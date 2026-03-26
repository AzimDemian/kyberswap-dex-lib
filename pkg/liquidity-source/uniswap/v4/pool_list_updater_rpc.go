package uniswapv4

import (
	"context"
	"os"
	"time"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	abis "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/abi"
	utilabi "github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/abi"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

const rpcUpdaterBatchSize = 100

type poolsListJSON struct {
	Pools []string `json:"pools"`
}

// getPoolsFromRPC fetches pool data for the next batch of pool IDs from list_of_pools.json using RPC.
// It returns the pools, the updated index into the list, and any error.
func (u *PoolsListUpdater) getPoolsFromRPC(ctx context.Context, fromIndex int, maxPools int) ([]entity.Pool, int, error) {
	poolIDs, err := u.loadPoolIDs()
	if err != nil {
		return nil, fromIndex, err
	}

	if fromIndex >= len(poolIDs) {
		// All pools from the list have already been processed.
		return nil, fromIndex, nil
	}

	end := fromIndex + maxPools
	if end > len(poolIDs) {
		end = len(poolIDs)
	}
	batch := poolIDs[fromIndex:end]

	initEvents, err := u.fetchInitializeEvents(ctx, batch)
	if err != nil {
		return nil, fromIndex, err
	}

	if len(initEvents) == 0 {
		logger.WithFields(logger.Fields{
			"dexId":       u.config.DexID,
			"requestedIDs": len(batch),
		}).Warn("no Initialize events found for requested pool IDs")
		return nil, end, nil
	}

	// Collect unique token addresses (excluding zero address / native ETH)
	tokenSet := make(map[common.Address]struct{})
	for _, ev := range initEvents {
		if !isZeroAddress(ev.Currency0) {
			tokenSet[ev.Currency0] = struct{}{}
		}
		if !isZeroAddress(ev.Currency1) {
			tokenSet[ev.Currency1] = struct{}{}
		}
	}

	uniqueTokens := make([]common.Address, 0, len(tokenSet))
	for addr := range tokenSet {
		uniqueTokens = append(uniqueTokens, addr)
	}

	decimalsMap, err := u.fetchTokenDecimals(ctx, uniqueTokens)
	if err != nil {
		return nil, fromIndex, err
	}

	chainID := valueobject.ChainID(u.config.ChainID)
	pools := make([]entity.Pool, 0, len(initEvents))
	for _, ev := range initEvents {
		pool := buildPoolFromInitEvent(ev, decimalsMap, u.config, chainID)
		pools = append(pools, pool)
	}

	return pools, end, nil
}

// loadPoolIDs reads pool IDs from the configured file or the embedded list_of_pools.json.
func (u *PoolsListUpdater) loadPoolIDs() ([]string, error) {
	var data []byte
	if u.config.PoolsFile != "" {
		var err error
		data, err = os.ReadFile(u.config.PoolsFile)
		if err != nil {
			return nil, err
		}
	} else {
		data = embeddedPoolsListJSON
	}

	var list poolsListJSON
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list.Pools, nil
}

// fetchInitializeEvents fetches Initialize events from the PoolManager for the given pool IDs.
// Pool IDs are processed in chunks of rpcUpdaterBatchSize to stay within provider topic limits.
func (u *PoolsListUpdater) fetchInitializeEvents(ctx context.Context, poolIDs []string) ([]*abis.UniswapV4PoolManagerInitialize, error) {
	ethClient := u.ethrpcClient.GetETHClient()
	filterer, err := abis.NewUniswapV4PoolManagerFilterer(
		common.HexToAddress(u.config.PoolManagerAddress), ethClient,
	)
	if err != nil {
		return nil, err
	}

	var result []*abis.UniswapV4PoolManagerInitialize

	for i := 0; i < len(poolIDs); i += rpcUpdaterBatchSize {
		end := i + rpcUpdaterBatchSize
		if end > len(poolIDs) {
			end = len(poolIDs)
		}
		chunk := poolIDs[i:end]

		ids := make([][32]byte, 0, len(chunk))
		for _, id := range chunk {
			ids = append(ids, stringToBytes32(id))
		}

		iter, err := filterer.FilterInitialize(&bind.FilterOpts{Context: ctx}, ids, nil, nil)
		if err != nil {
			return nil, err
		}

		for iter.Next() {
			ev := iter.Event
			result = append(result, ev)
		}
		if err := iter.Error(); err != nil {
			_ = iter.Close()
			return nil, err
		}
		_ = iter.Close()
	}

	return result, nil
}

// fetchTokenDecimals batch-fetches ERC20 decimals for the given token addresses.
func (u *PoolsListUpdater) fetchTokenDecimals(ctx context.Context, addresses []common.Address) (map[common.Address]uint8, error) {
	result := make(map[common.Address]uint8, len(addresses))
	if len(addresses) == 0 {
		return result, nil
	}

	decimals := make([]uint8, len(addresses))
	req := u.ethrpcClient.NewRequest().SetContext(ctx)

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
		result[addr] = decimals[i]
	}
	return result, nil
}

// buildPoolFromInitEvent constructs an entity.Pool from a parsed Initialize event.
func buildPoolFromInitEvent(
	ev *abis.UniswapV4PoolManagerInitialize,
	decimalsMap map[common.Address]uint8,
	cfg *Config,
	chainID valueobject.ChainID,
) entity.Pool {
	token0Addr := currencyToToken(ev.Currency0, chainID)
	token1Addr := currencyToToken(ev.Currency1, chainID)

	token0Decimals := decimalsMap[ev.Currency0]
	token1Decimals := decimalsMap[ev.Currency1]

	fee := uint32(ev.Fee.Uint64())
	tickSpacing := int32(ev.TickSpacing.Int64())

	staticExtra := StaticExtra{
		IsNative:               [2]bool{isZeroAddress(ev.Currency0), isZeroAddress(ev.Currency1)},
		Fee:                    fee,
		TickSpacing:            tickSpacing,
		HooksAddress:           ev.Hooks,
		UniversalRouterAddress: common.HexToAddress(cfg.UniversalRouterAddress),
		Permit2Address:         common.HexToAddress(cfg.Permit2Address),
		Multicall3Address:      common.HexToAddress(cfg.Multicall3Address),
	}
	staticExtraBytes, _ := json.Marshal(staticExtra)

	hook, _ := GetHook(ev.Hooks, &HookParam{Cfg: cfg})

	poolAddress := poolIDToString(ev.Id)

	return entity.Pool{
		Address:   poolAddress,
		SwapFee:   float64(fee),
		Exchange:  hook.GetExchange(),
		Type:      DexType,
		Timestamp: time.Now().Unix(),
		Reserves:  entity.PoolReserves{"0", "0"},
		Tokens: []*entity.PoolToken{
			{Address: token0Addr, Decimals: token0Decimals, Swappable: true},
			{Address: token1Addr, Decimals: token1Decimals, Swappable: true},
		},
		Extra:       "{}",
		StaticExtra: string(staticExtraBytes),
	}
}

// poolIDToString converts a bytes32 pool ID to a hex string (matches the 0x-prefixed format used by pool_factory.go).
func poolIDToString(id [32]byte) string {
	return hexutil.Encode(id[:])
}

// isZeroAddress returns true if the address is the zero address (native ETH in UniV4).
func isZeroAddress(addr common.Address) bool {
	return addr == (common.Address{})
}
