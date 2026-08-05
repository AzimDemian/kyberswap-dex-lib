package integral

import (
	"context"
	"math/big"
	"sort"
	"strconv"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/kutils"
	"github.com/KyberNetwork/logger"
	v3Utils "github.com/KyberNetwork/uniswapv3-sdk-uint256/utils"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	sourcePool "github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/ticklens"
)

// PopulatedTick mirrors the ITickLens.PopulatedTick struct returned by the Algebra TickLens.
type PopulatedTick struct {
	Tick           *big.Int
	LiquidityNet   *big.Int
	LiquidityGross *big.Int
}

func (d *PoolTracker) getPoolTicksFromSC(ctx context.Context, pool entity.Pool, param sourcePool.GetNewPoolStateParams) ([]TickResp, error) {
	changedTicks := ticklens.GetChangedTicks(param.Logs)
	if len(changedTicks) == 0 {
		// No changed ticks in the logs (e.g. bootstrap / RPC-discovered pool): discover the
		// full tick set on-chain by walking Algebra's active-tick linked list via the TickLens.
		return d.getAllTicksFromSC(ctx, pool.Address)
	}

	logger.Infof("Fetch changed ticks (%v)", changedTicks)

	rpcRequest := d.EthrpcClient.NewRequest()
	rpcRequest.SetContext(ctx)
	populatedTicks := make([]Tick, len(changedTicks))
	for i, tick := range changedTicks {
		rpcRequest.AddCall(&ethrpc.Call{
			ABI:    poolV12ABI,
			Target: pool.Address,
			Method: poolTicksMethod,
			Params: []any{big.NewInt(tick)},
		}, []any{&populatedTicks[i]})
	}

	resp, err := rpcRequest.Aggregate()
	if err != nil {
		return nil, err
	}

	ticks := make([]TickResp, 0, len(resp.Request.Calls))
	for i, result := range resp.Result {
		if !result {
			logger.Errorf("failed to try multicall with param: %v", resp.Request.Calls[i].Params)
			continue
		}

		if populatedTicks[i].LiquidityTotal.Sign() == 1 {
			ticks = append(ticks, TickResp{
				TickIdx:        strconv.FormatInt(changedTicks[i], 10),
				LiquidityGross: populatedTicks[i].LiquidityTotal.String(),
				LiquidityNet:   populatedTicks[i].LiquidityDelta.String(),
			})
		}
	}

	// if we only fetched some ticks, then update them to the original ticks and return
	if len(changedTicks) > 0 {
		var extra Extra
		if err := json.Unmarshal([]byte(pool.Extra), &extra); err != nil {
			return nil, err
		}

		// ticklens contract might return unchanged tick (in the same word), so need to filter them out
		changedTickSet := mapset.NewThreadUnsafeSet(changedTicks...)
		changedTickMap := make(map[int64]TickResp, len(changedTicks))
		for _, t := range ticks {
			tIdx, err := kutils.Atoi[int64](t.TickIdx)
			if err == nil && changedTickSet.ContainsOne(tIdx) {
				changedTickMap[tIdx] = t
			}
		}

		combined := make([]TickResp, 0, len(changedTicks)+len(extra.Ticks))
		for _, t := range extra.Ticks {
			if tick, ok := changedTickMap[int64(t.Index)]; ok {
				// changed, use new value
				combined = append(combined, tick)
				delete(changedTickMap, int64(t.Index))
			} else if changedTickSet.ContainsOne(int64(t.Index)) {
				// some changed ticks might be consumed entirely and are not in `changedTickMap`, delete them
				logger.Debugf("deleted tick %v %v", pool.Address, t)
			} else {
				// use old value
				combined = append(combined, TickResp{
					TickIdx:        strconv.Itoa(t.Index),
					LiquidityGross: t.LiquidityGross.String(),
					LiquidityNet:   t.LiquidityNet.Dec(),
				})
			}
		}

		// remaining (newly created ticks)
		for _, tick := range changedTickMap {
			combined = append(combined, tick)
		}
		ticks = combined
	}

	sort.SliceStable(ticks, func(i, j int) bool {
		iTick, _ := strconv.Atoi(ticks[i].TickIdx)
		jTick, _ := strconv.Atoi(ticks[j].TickIdx)

		return iTick < jTick
	})

	return ticks, nil
}

// getAllTicksFromSC discovers every initialized tick of a pool from the on-chain TickLens.
//
// Algebra stores ticks as a doubly-linked list (prevTick/nextTick) rather than a Uniswap-style
// word bitmap, so instead of scanning the whole tick range word-by-word we let the TickLens walk
// the list for us. Starting from the always-initialized MIN_TICK sentinel and walking upward,
// getNextActiveTicks follows nextTick pointers on-chain and returns the entire list in a single
// eth_call for any pool with fewer than fetchTicksAmount initialized ticks (i.e. virtually all
// pools), so this needs just one node request in the common case.
func (d *PoolTracker) getAllTicksFromSC(ctx context.Context, poolAddress string) ([]TickResp, error) {
	poolAddr := common.HexToAddress(poolAddress)
	// MIN_TICK is a self-referencing sentinel (prevTick == MIN_TICK), so it's a valid, always
	// initialized starting point for getNextActiveTicks.
	startingTick := big.NewInt(int64(v3Utils.MinTick))

	ticksByIdx := make(map[int64]TickResp)
	for {
		var populatedTicks []PopulatedTick
		req := d.EthrpcClient.NewRequest().SetContext(ctx)
		req.AddCall(&ethrpc.Call{
			ABI:    ticklensABI,
			Target: d.config.TickLensAddress,
			Method: tickLensGetNextActiveTicksMethod,
			Params: []any{poolAddr, startingTick, big.NewInt(fetchTicksAmount), true},
		}, []any{&populatedTicks})

		if _, err := req.Call(); err != nil {
			return nil, err
		}

		if len(populatedTicks) == 0 {
			break
		}

		for _, pt := range populatedTicks {
			// skip the MIN_TICK/MAX_TICK sentinels and any uninitialized tick
			if pt.LiquidityGross == nil || pt.LiquidityGross.Sign() <= 0 {
				continue
			}

			tickIdx := pt.Tick.Int64()
			ticksByIdx[tickIdx] = TickResp{
				TickIdx:        strconv.FormatInt(tickIdx, 10),
				LiquidityGross: pt.LiquidityGross.String(),
				LiquidityNet:   pt.LiquidityNet.String(),
			}
		}

		// fewer than requested means the upper boundary (MAX_TICK) was reached: all ticks fetched
		if len(populatedTicks) < fetchTicksAmount {
			break
		}

		// otherwise continue from the last tick; it's returned again (inclusive) but deduped by the map
		lastTick := populatedTicks[len(populatedTicks)-1].Tick
		if lastTick.Cmp(startingTick) == 0 {
			break
		}
		startingTick = lastTick
	}

	ticks := make([]TickResp, 0, len(ticksByIdx))
	for _, t := range ticksByIdx {
		ticks = append(ticks, t)
	}

	sort.SliceStable(ticks, func(i, j int) bool {
		iTick, _ := strconv.Atoi(ticks[i].TickIdx)
		jTick, _ := strconv.Atoi(ticks[j].TickIdx)

		return iTick < jTick
	})

	return ticks, nil
}
