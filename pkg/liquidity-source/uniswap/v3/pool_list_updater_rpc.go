package uniswapv3

import (
	"context"
	"math/big"
	"time"

	"github.com/KyberNetwork/ethrpc"
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
func (d *PoolsListUpdater) getPoolsFromRPC(ctx context.Context, fromIndex, maxPools int) ([]entity.Pool, int, error) {
	poolAddresses := d.config.StaticPoolList
	if fromIndex >= len(poolAddresses) {
		return nil, fromIndex, nil
	}

	end := fromIndex + maxPools
	if end > len(poolAddresses) {
		end = len(poolAddresses)
	}
	batch := poolAddresses[fromIndex:end]

	poolData, err := d.fetchPoolData(ctx, batch)
	if err != nil {
		return nil, fromIndex, err
	}

	// Collect unique token addresses
	tokenSet := make(map[common.Address]struct{})
	for _, pd := range poolData {
		if pd.token0 != nil {
			tokenSet[*pd.token0] = struct{}{}
		}
		if pd.token1 != nil {
			tokenSet[*pd.token1] = struct{}{}
		}
	}
	uniqueTokens := make([]common.Address, 0, len(tokenSet))
	for addr := range tokenSet {
		uniqueTokens = append(uniqueTokens, addr)
	}

	decimalsMap, err := d.fetchTokenDecimals(ctx, uniqueTokens)
	if err != nil {
		return nil, fromIndex, err
	}

	pools := make([]entity.Pool, 0, len(batch))
	for i, addr := range batch {
		pd := poolData[i]
		if pd.token0 == nil || pd.token1 == nil || pd.fee == nil || pd.tickSpacing == nil {
			continue
		}
		pools = append(pools, buildPoolFromRPCData(addr, pd, decimalsMap, d.config))
	}

	return pools, end, nil
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

// fetchTokenDecimals batch-fetches ERC20 decimals for the given token addresses.
func (d *PoolsListUpdater) fetchTokenDecimals(ctx context.Context, addresses []common.Address) (map[common.Address]uint8, error) {
	result := make(map[common.Address]uint8, len(addresses))
	if len(addresses) == 0 {
		return result, nil
	}

	decimals := make([]*big.Int, len(addresses))
	req := d.ethrpcClient.NewRequest().SetContext(ctx)
	for i, addr := range addresses {
		req.AddCall(&ethrpc.Call{
			ABI:    utilabi.Erc20ABI,
			Target: addr.Hex(),
			Method: utilabi.Erc20DecimalsMethod,
		}, []any{&decimals[i]})
	}

	if _, err := req.TryAggregate(); err != nil {
		return nil, err
	}

	for i, addr := range addresses {
		if decimals[i] != nil {
			result[addr] = uint8(decimals[i].Uint64())
		}
	}
	return result, nil
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
		Address:   address,
		SwapFee:   float64(pd.fee.Uint64()),
		Exchange:  cfg.DexID,
		Type:      DexTypeUniswapV3,
		Timestamp: time.Now().Unix(),
		Reserves:  entity.PoolReserves{"0", "0"},
		Tokens: []*entity.PoolToken{
			{Address: pd.token0.Hex(), Decimals: token0Decimals, Swappable: true},
			{Address: pd.token1.Hex(), Decimals: token1Decimals, Swappable: true},
		},
		Extra:       string(extraBytes),
		StaticExtra: string(staticBytes),
	}
}
