package uniswapv4

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/goccy/go-json"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	uniswapv3 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3"
	tickspkg "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3/ticks"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/eth"
)

type LogsByPoolAddr map[string][]ethtypes.Log

type rpcBatchOut struct {
	Liquidity    *big.Int
	Slot0        Slot0Data
	TickSpacing  int32
	Reserves     entity.PoolReserves
	HookExtra    json.RawMessage
	HooksAddress string
	Exchange     string
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
		key := strings.ToLower(p.Address)
		logs := logsByPool[key]
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
			affectedByPool[key] = affected
		}
	}

	refetchedTicks, err := t.queryTicksFromRPCBatch(ctx, affectedByPool, snapshotBlock)
	if err != nil {
		return nil, err
	}

	out := make([]entity.Pool, 0, len(pools))

	for _, p := range pools {
		key := strings.ToLower(p.Address)

		rpc := rpcStates[key]
		if rpc == nil {
			out = append(out, p)
			continue
		}

		logs := logsByPool[key]

		updatedTicks, err := t.mergeTicksFromExtraAndRefetch(p, refetchedTicks[key])
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
		sort.Slice(entityPoolTicks, func(i, j int) bool {
			return entityPoolTicks[i].Index < entityPoolTicks[j].Index
		})

		p.SwapFee, _ = rpc.Slot0.LpFee.Float64()

		extraBytes, err := json.Marshal(Extra{
			Extra: &uniswapv3.Extra{
				Liquidity:    rpc.Liquidity,
				TickSpacing:  uint64(rpc.TickSpacing),
				SqrtPriceX96: rpc.Slot0.SqrtPriceX96,
				Tick:         rpc.Slot0.Tick,
				Ticks:        entityPoolTicks,
			},
			HookExtra: rpc.HookExtra,
		})
		if err != nil {
			return nil, err
		}

		p.Extra = string(extraBytes)
		if rpc.Reserves != nil {
			p.Reserves = rpc.Reserves
		} else {
			reserve0, reserve1 := EstimateReservesFromTicks(rpc.Slot0.SqrtPriceX96, entityPoolTicks)
			p.Reserves = entity.PoolReserves{reserve0.String(), reserve1.String()}
		}
		p.BlockNumber = snapshotBlock
		p.Timestamp = t.estimateLastActivityTime(&p, logs, blockHeaders)
		p.Exchange = rpc.Exchange

		out = append(out, p)
	}

	return out, nil
}

func (t *PoolTracker) fetchRPCDataBatch(
	ctx context.Context,
	pools []entity.Pool,
	blockNumber uint64,
) (map[string]*rpcBatchOut, error) {
	req := t.ethrpcClient.NewRequest().SetContext(ctx)

	var bn big.Int
	if blockNumber > 0 {
		bn.SetUint64(blockNumber)
		req.SetBlockNumber(&bn)
	}

	n := len(pools)
	liq := make([]*big.Int, n)
	slot0 := make([]Slot0Data, n)

	staticExtras := make([]StaticExtra, n)

	for i := range pools {
		p := pools[i]

		_ = json.Unmarshal([]byte(p.StaticExtra), &staticExtras[i])

		req.AddCall(&ethrpc.Call{
			ABI:    stateViewABI,
			Target: t.config.StateViewAddress,
			Method: "getLiquidity",
			Params: []any{stringToBytes32(p.Address)},
		}, []any{&liq[i]})

		req.AddCall(&ethrpc.Call{
			ABI:    stateViewABI,
			Target: t.config.StateViewAddress,
			Method: "getSlot0",
			Params: []any{stringToBytes32(p.Address)},
		}, []any{&slot0[i]})
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
		key := strings.ToLower(pools[i].Address)

		if liq[i] == nil {
			liq[i] = big.NewInt(0)
		}

		hookAddress := staticExtras[i].HooksAddress
		hookParam := &HookParam{Cfg: t.config, RpcClient: t.ethrpcClient, Pool: &pools[i], BlockNumber: &bn}
		hook, _ := GetHook(hookAddress, hookParam)

		// A nil result is not an error: it means the hook has no opinion on reserves,
		// and the caller estimates them from ticks instead.
		reserves, hookErr := hook.GetReserves(ctx, hookParam)
		if hookErr != nil {
			return nil, hookErr
		}

		hookExtra, hookErr := hook.Track(ctx, hookParam)
		if hookErr != nil {
			return nil, hookErr
		}

		out[key] = &rpcBatchOut{
			Liquidity:    liq[i],
			Slot0:        slot0[i],
			TickSpacing:  staticExtras[i].TickSpacing,
			Reserves:     reserves,
			HookExtra:    hookExtra,
			HooksAddress: hookAddress.Hex(),
			Exchange:     hook.GetExchange(),
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
		poolKey string
		tickIdx int
	}

	var metas []meta
	for poolKey, ticks := range affected {
		for _, tk := range ticks {
			metas = append(metas, meta{
				poolKey: poolKey,
				tickIdx: tk,
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

		req := t.ethrpcClient.NewRequest().SetContext(ctx)
		if blockNumber > 0 {
			var bn big.Int
			bn.SetUint64(blockNumber)
			req.SetBlockNumber(&bn)
		}

		for i := range chunk {
			req.AddCall(&ethrpc.Call{
				ABI:    stateViewABI,
				Target: t.config.StateViewAddress,
				Method: "getTickLiquidity",
				Params: []any{stringToBytes32(chunk[i].poolKey), big.NewInt(int64(chunk[i].tickIdx))},
			}, []any{&resps[i]})
		}

		if _, err := req.Aggregate(); err != nil {
			if blockNumber > 0 && tickspkg.IsMissingTrieNodeError(err) {
				return t.queryTicksFromRPCBatch(ctx, affected, 0)
			}
			return nil, fmt.Errorf("aggregate ticks: %w", err)
		}

		for i := range chunk {
			poolKey := chunk[i].poolKey
			result[poolKey] = append(result[poolKey], tickspkg.Tick{
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
	affected := make(map[int]struct{})

	for _, event := range logs {
		if len(event.Topics) == 0 || eth.IsZeroAddress(event.Address) {
			continue
		}

		switch event.Topics[0] {
		case poolManagerABI.Events["ModifyLiquidity"].ID:
			ml, err := poolManagerFilterer.ParseModifyLiquidity(event)
			if err != nil {
				return nil, err
			}

			affected[int(ml.TickLower.Int64())] = struct{}{}
			affected[int(ml.TickUpper.Int64())] = struct{}{}
		default:
		}
	}

	return lo.Keys(affected), nil
}
