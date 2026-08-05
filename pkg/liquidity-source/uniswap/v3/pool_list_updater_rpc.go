package uniswapv3

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3/abis"
	utilabi "github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/abi"
)

type poolRPCData struct {
	token0      *common.Address
	token1      *common.Address
	fee         *big.Int
	tickSpacing *big.Int
}

// getPoolsFromRPC fetches pool metadata for the next batch from config.StaticPoolList using direct RPC calls.
// Returns successfully built pools, addresses of pools that failed metadata fetch (for subgraph fallback),
// and the next index to process.
func (d *PoolsListUpdater) getPoolsFromRPC(ctx context.Context, fromIndex, maxPools int) ([]entity.Pool, []string, int, error) {
	poolAddresses := d.config.StaticPoolList
	if fromIndex >= len(poolAddresses) {
		return nil, nil, fromIndex, nil
	}

	end := min(fromIndex+maxPools, len(poolAddresses))
	batch := poolAddresses[fromIndex:end]

	poolData, err := d.fetchPoolData(ctx, batch)
	if err != nil {
		return nil, nil, fromIndex, err
	}

	zeroAddr := common.Address{}
	valid := make([]bool, len(batch))
	var failedAddresses []string
	tokenSet := make(map[common.Address]struct{})

	for i, pd := range poolData {
		// token0/token1 are preallocated pointers (new(common.Address)), so nil check is
		// insufficient — compare the dereferenced value to detect zero-address failures.
		if pd.token0 == nil || *pd.token0 == zeroAddr ||
			pd.token1 == nil || *pd.token1 == zeroAddr ||
			pd.fee == nil || pd.tickSpacing == nil {
			logger.WithFields(logger.Fields{
				"dexId": d.config.DexID,
				"pool":  batch[i],
			}).Warn("[getPoolsFromRPC] pool has zero/nil metadata, will attempt subgraph fallback")
			failedAddresses = append(failedAddresses, strings.ToLower(batch[i]))
			continue
		}
		valid[i] = true
		tokenSet[*pd.token0] = struct{}{}
		tokenSet[*pd.token1] = struct{}{}
	}

	uniqueTokens := make([]common.Address, 0, len(tokenSet))
	for addr := range tokenSet {
		uniqueTokens = append(uniqueTokens, addr)
	}

	decimalsMap := d.fetchTokenDecimals(ctx, uniqueTokens)

	pools := make([]entity.Pool, 0, len(batch))
	for i, addr := range batch {
		if !valid[i] {
			continue
		}
		pools = append(pools, buildPoolFromRPCData(addr, poolData[i], decimalsMap, d.config))
	}

	logger.WithFields(logger.Fields{
		"dexId":  d.config.DexID,
		"total":  len(batch),
		"built":  len(pools),
		"failed": len(failedAddresses),
	}).Info("[getPoolsFromRPC] batch complete")

	return pools, failedAddresses, end, nil
}

// fetchPoolData batch-calls token0/token1/fee/tickSpacing on each pool contract.
func (d *PoolsListUpdater) fetchPoolData(ctx context.Context, addresses []string) ([]poolRPCData, error) {
	result := make([]poolRPCData, len(addresses))

	for i := 0; i < len(addresses); i += rpcChunkSize {
		end := i + rpcChunkSize
		if end > len(addresses) {
			end = len(addresses)
		}
		chunk := addresses[i:end]

		token0s := make([]*common.Address, len(chunk))
		token1s := make([]*common.Address, len(chunk))
		fees := make([]*big.Int, len(chunk))
		tickSpacings := make([]*big.Int, len(chunk))

		req := d.ethrpcClient.NewRequest().SetContext(ctx)
		for j, addr := range chunk {
			token0s[j] = new(common.Address)
			token1s[j] = new(common.Address)
			req.AddCall(&ethrpc.Call{ABI: abis.UniswapV3PoolABI, Target: addr, Method: methodToken0}, []any{token0s[j]})
			req.AddCall(&ethrpc.Call{ABI: abis.UniswapV3PoolABI, Target: addr, Method: methodToken1}, []any{token1s[j]})
			req.AddCall(&ethrpc.Call{ABI: abis.UniswapV3PoolABI, Target: addr, Method: methodFee}, []any{&fees[j]})
			req.AddCall(&ethrpc.Call{ABI: abis.UniswapV3PoolABI, Target: addr, Method: methodTickSpacing}, []any{&tickSpacings[j]})
		}

		if _, err := req.TryAggregate(); err != nil {
			return nil, err
		}

		for j := range chunk {
			result[i+j] = poolRPCData{
				token0:      token0s[j],
				token1:      token1s[j],
				fee:         fees[j],
				tickSpacing: tickSpacings[j],
			}
		}
	}

	return result, nil
}

// fetchTokenDecimals batch-fetches ERC20 decimals for the given token addresses, chunked
// by rpcChunkSize. If any chunk fails, those tokens fall back to defaultTokenDecimals (18)
// and are logged. Never returns an error — partial failures are absorbed gracefully.
func (d *PoolsListUpdater) fetchTokenDecimals(ctx context.Context, addresses []common.Address) map[common.Address]uint8 {
	result := make(map[common.Address]uint8, len(addresses))
	if len(addresses) == 0 {
		return result
	}

	totalFallback := 0

	for i := 0; i < len(addresses); i += rpcChunkSize {
		end := min(i+rpcChunkSize, len(addresses))
		chunk := addresses[i:end]

		decimals := make([]uint8, len(chunk))
		req := d.ethrpcClient.NewRequest().SetContext(ctx)
		for j, addr := range chunk {
			req.AddCall(&ethrpc.Call{
				ABI:    utilabi.Erc20ABI,
				Target: addr.Hex(),
				Method: utilabi.Erc20DecimalsMethod,
			}, []any{&decimals[j]})
		}

		if _, err := req.TryAggregate(); err != nil {
			// Log per-token addresses so operators can investigate
			addrs := make([]string, len(chunk))
			for k, a := range chunk {
				addrs[k] = a.Hex()
			}
			logger.WithFields(logger.Fields{
				"dexId":  d.config.DexID,
				"tokens": addrs,
				"error":  err,
			}).Warn("[fetchTokenDecimals] chunk failed, using defaultTokenDecimals for affected tokens")

			for _, addr := range chunk {
				result[addr] = defaultTokenDecimals
			}
			totalFallback += len(chunk)
			continue
		}

		for j, addr := range chunk {
			result[addr] = decimals[j]
		}
	}

	if totalFallback > 0 {
		logger.WithFields(logger.Fields{
			"dexId":    d.config.DexID,
			"fallback": totalFallback,
			"total":    len(addresses),
		}).Warn("[fetchTokenDecimals] some tokens fell back to defaultTokenDecimals")
	}

	return result
}

// buildPoolFromRPCData constructs an entity.Pool from direct RPC pool data.
func buildPoolFromRPCData(
	address string,
	pd poolRPCData,
	decimalsMap map[common.Address]uint8,
	cfg *Config,
) entity.Pool {
	token0Decimals := decimalsMap[*pd.token0]
	token1Decimals := decimalsMap[*pd.token1]

	extraField := Extra{
		TickSpacing: pd.tickSpacing.Uint64(),
	}
	staticField := StaticExtra{
		PoolId: address,
	}

	extraBytes, _ := json.Marshal(extraField)
	staticBytes, _ := json.Marshal(staticField)

	return entity.Pool{
		Address:   strings.ToLower(address),
		SwapFee:   float64(pd.fee.Uint64()),
		Exchange:  cfg.DexID,
		Type:      DexTypeUniswapV3,
		Timestamp: time.Now().Unix(),
		Reserves:  entity.PoolReserves{"0", "0"},
		Tokens: []*entity.PoolToken{
			{Address: strings.ToLower(pd.token0.Hex()), Decimals: token0Decimals, Swappable: true},
			{Address: strings.ToLower(pd.token1.Hex()), Decimals: token1Decimals, Swappable: true},
		},
		Extra:       string(extraBytes),
		StaticExtra: string(staticBytes),
	}
}
