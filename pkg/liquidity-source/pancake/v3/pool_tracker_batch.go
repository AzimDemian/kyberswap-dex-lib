package pancakev3

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/goccy/go-json"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	tickspkg "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3/ticks"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/abi"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/eth"
)

type LogsByPoolAddr map[string][]ethtypes.Log

type rpcBatchOut struct {
	Liquidity   *big.Int
	Slot0       Slot0
	TickSpacing *big.Int
	Reserve0    *big.Int
	Reserve1    *big.Int
}

func pickSnapshotBlock(headBlock uint64, logsByPool LogsByPoolAddr, blockHeaders map[uint64]entity.BlockHeader) uint64 {
	var snapshot uint64

	for bn := range blockHeaders {
		if bn > snapshot {
			snapshot = bn
		}
	}

	if snapshot == 0 {
		for _, logs := range logsByPool {
			for _, lg := range logs {
				if lg.BlockNumber > snapshot {
					snapshot = lg.BlockNumber
				}
			}
		}
	}

	if snapshot == 0 {
		snapshot = headBlock
	}

	return snapshot
}

func (t *PoolTracker) GetNewStates(
	ctx context.Context,
	pools []entity.Pool,
	logsByPool LogsByPoolAddr,
	blockHeaders map[uint64]entity.BlockHeader,
) ([]entity.Pool, error) {
	if len(pools) == 0 {
		return nil, nil
	}

	headBlock, err := t.ethrpcClient.GetBlockNumber(ctx)
	if err != nil {
		return nil, err
	}

	snapshotBlock := pickSnapshotBlock(headBlock, logsByPool, blockHeaders)

	rpcStates, err := t.fetchRPCDataBatch(ctx, pools, snapshotBlock)
	if err != nil {
		return nil, err
	}

	affectedByPool := make(map[string][]int, 64)

	for _, p := range pools {
		addrKey := strings.ToLower(p.Address)
		logs := logsByPool[addrKey]
		if len(logs) == 0 {
			continue
		}

		affected, err := t.getAffectedTickIdsFromLogsBatch(logs)
		if err != nil {
			logger.WithFields(logger.Fields{
				"poolAddress": p.Address,
				"error":       err,
			}).Warn("failed to parse affected ticks from logs")
			continue
		}
		if len(affected) > 0 {
			affectedByPool[addrKey] = affected
		}
	}

	refetchedTicks, err := t.queryTicksFromRPCBatch(ctx, affectedByPool, snapshotBlock)
	if err != nil {
		return nil, err
	}

	out := make([]entity.Pool, 0, len(pools))
	for _, p := range pools {
		addrKey := strings.ToLower(p.Address)

		rpc := rpcStates[addrKey]
		if rpc == nil {
			out = append(out, p)
			continue
		}

		logs := logsByPool[addrKey]

		updatedTicks, err := t.mergeTicksFromExtraAndRefetch(p, refetchedTicks[addrKey])
		if err != nil {
			logger.WithFields(logger.Fields{
				"poolAddress": p.Address,
				"error":       err,
			}).Warn("failed to merge ticks; updating pool without tick refresh")
			updatedTicks = nil
		}

		entityPoolTicks := make([]Tick, 0, len(updatedTicks))
		for _, tk := range updatedTicks {
			if tk.LiquidityGross == nil || tk.LiquidityGross.Sign() == 0 {
				continue
			}
			entityPoolTicks = append(entityPoolTicks, Tick{
				Index:          tk.TickIdx,
				LiquidityGross: tk.LiquidityGross,
				LiquidityNet:   tk.LiquidityNet,
			})
		}
		sort.Slice(entityPoolTicks, func(i, j int) bool { return entityPoolTicks[i].Index < entityPoolTicks[j].Index })

		extraBytes, err := json.Marshal(Extra{
			Liquidity:    rpc.Liquidity,
			SqrtPriceX96: rpc.Slot0.SqrtPriceX96,
			TickSpacing:  rpc.TickSpacing.Uint64(),
			Tick:         rpc.Slot0.Tick,
			Ticks:        entityPoolTicks,
		})
		if err != nil {
			return nil, err
		}

		p.Extra = string(extraBytes)
		p.Reserves = entity.PoolReserves{
			rpc.Reserve0.String(),
			rpc.Reserve1.String(),
		}

		p.BlockNumber = snapshotBlock

		p.Timestamp = t.estimateLastActivityTime(&p, logs, blockHeaders)

		out = append(out, p)
	}

	return out, nil
}

func (t *PoolTracker) fetchRPCDataBatch(
	ctx context.Context,
	pools []entity.Pool,
	blockNumber uint64,
) (map[string]*rpcBatchOut, error) {
	req := t.ethrpcClient.NewRequest()
	req.SetContext(ctx)

	if blockNumber > 0 {
		var bn big.Int
		bn.SetUint64(blockNumber)
		req.SetBlockNumber(&bn)
	}

	n := len(pools)
	liq := make([]*big.Int, n)
	slot0 := make([]Slot0, n)
	spacing := make([]*big.Int, n)
	r0 := make([]*big.Int, n)
	r1 := make([]*big.Int, n)

	for i := range pools {
		p := pools[i]

		req.AddCall(&ethrpc.Call{
			ABI:    pancakeV3PoolABI,
			Target: p.Address,
			Method: methodGetLiquidity,
		}, []any{&liq[i]})

		req.AddCall(&ethrpc.Call{
			ABI:    pancakeV3PoolABI,
			Target: p.Address,
			Method: methodGetSlot0,
		}, []any{&slot0[i]})

		req.AddCall(&ethrpc.Call{
			ABI:    pancakeV3PoolABI,
			Target: p.Address,
			Method: methodTickSpacing,
		}, []any{&spacing[i]})

		if len(p.Tokens) == 2 {
			req.AddCall(&ethrpc.Call{
				ABI:    abi.Erc20ABI,
				Target: p.Tokens[0].Address,
				Method: abi.Erc20BalanceOfMethod,
				Params: []any{common.HexToAddress(p.Address)},
			}, []any{&r0[i]})

			req.AddCall(&ethrpc.Call{
				ABI:    abi.Erc20ABI,
				Target: p.Tokens[1].Address,
				Method: abi.Erc20BalanceOfMethod,
				Params: []any{common.HexToAddress(p.Address)},
			}, []any{&r1[i]})
		}
	}

	_, err := req.TryAggregate()
	if err != nil {
		if blockNumber > 0 && tickspkg.IsMissingTrieNodeError(err) {
			return t.fetchRPCDataBatch(ctx, pools, 0)
		}
		return nil, err
	}

	out := make(map[string]*rpcBatchOut, n)
	for i := range pools {
		addrKey := strings.ToLower(pools[i].Address)

		if liq[i] == nil {
			liq[i] = big.NewInt(0)
		}
		if spacing[i] == nil {
			spacing[i] = big.NewInt(0)
		}
		if r0[i] == nil {
			r0[i] = big.NewInt(0)
		}
		if r1[i] == nil {
			r1[i] = big.NewInt(0)
		}

		out[addrKey] = &rpcBatchOut{
			Liquidity:   liq[i],
			Slot0:       slot0[i],
			TickSpacing: spacing[i],
			Reserve0:    r0[i],
			Reserve1:    r1[i],
		}
	}

	return out, nil
}

func (t *PoolTracker) queryTicksFromRPCBatch(
	ctx context.Context,
	affected map[string][]int,
	blockNumber uint64,
) (map[string][]tickspkg.Tick, error) {
	if len(affected) == 0 {
		return map[string][]tickspkg.Tick{}, nil
	}

	type meta struct {
		poolAddr string
		tickIdx  int
		outIdx   int
	}

	var metas []meta
	for poolAddr, ticks := range affected {
		for _, tk := range ticks {
			metas = append(metas, meta{
				poolAddr: poolAddr,
				tickIdx:  tk,
			})
		}
	}
	if len(metas) == 0 {
		return map[string][]tickspkg.Tick{}, nil
	}

	const maxCallsPerAggregate = 1500

	result := make(map[string][]tickspkg.Tick, len(affected))

	for start := 0; start < len(metas); start += maxCallsPerAggregate {
		end := start + maxCallsPerAggregate
		if end > len(metas) {
			end = len(metas)
		}

		chunk := metas[start:end]
		resps := make([]TicksResp, len(chunk))

		req := t.ethrpcClient.NewRequest()
		req.SetContext(ctx)
		if blockNumber > 0 {
			var bn big.Int
			bn.SetUint64(blockNumber)
			req.SetBlockNumber(&bn)
		}

		for i := range chunk {
			req.AddCall(&ethrpc.Call{
				ABI:    pancakeV3PoolABI,
				Target: chunk[i].poolAddr,
				Method: methodTicks,
				Params: []any{big.NewInt(int64(chunk[i].tickIdx))},
			}, []any{&resps[i]})
		}

		if _, err := req.Aggregate(); err != nil {
			if blockNumber > 0 && tickspkg.IsMissingTrieNodeError(err) {
				return t.queryTicksFromRPCBatch(ctx, affected, 0)
			}
			return nil, fmt.Errorf("aggregate ticks: %w", err)
		}

		for i := range chunk {
			pool := chunk[i].poolAddr
			result[pool] = append(result[pool], tickspkg.Tick{
				TickIdx:        chunk[i].tickIdx,
				LiquidityGross: resps[i].LiquidityGross,
				LiquidityNet:   resps[i].LiquidityNet,
			})
		}
	}

	return result, nil
}

func (t *PoolTracker) mergeTicksFromExtraAndRefetch(
	p entity.Pool,
	refetched []tickspkg.Tick,
) (map[int]tickspkg.Tick, error) {
	tb, err := tickspkg.NewTicksBasedPool(p)
	if err != nil {
		return nil, err
	}

	for _, tk := range refetched {
		tb.Ticks[tk.TickIdx] = tk
	}

	return tb.Ticks, nil
}

func (t *PoolTracker) getAffectedTickIdsFromLogsBatch(logs []ethtypes.Log) ([]int, error) {
	affectedTickIds := make(map[int]struct{})

	for _, event := range logs {
		if len(event.Topics) == 0 || eth.IsZeroAddress(event.Address) {
			continue
		}

		switch event.Topics[0] {
		case pancakeV3PoolABI.Events["Mint"].ID,
			pancakeV3PoolABI.Events["Burn"].ID:
			lower, upper, delta, err := t.extractEventData(event)
			if err != nil {
				return nil, err
			}
			if delta.Sign() == 0 {
				continue
			}
			affectedTickIds[lower] = struct{}{}
			affectedTickIds[upper] = struct{}{}
		default:
		}
	}

	return lo.Keys(affectedTickIds), nil
}
