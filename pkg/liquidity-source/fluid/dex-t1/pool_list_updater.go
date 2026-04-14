package dexT1

import (
	"context"
	"strings"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	poollist "github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool/list"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

const (
	staticPoolBatchSize  = 100
	defaultTokenDecimals = 18
)

type PoolsListUpdater struct {
	config       Config
	ethrpcClient *ethrpc.Client
}

type Metadata struct {
	LastSyncPoolsLength   int `json:"lastSyncPoolsLength"`
	LastProcessedRPCIndex int `json:"lastProcessedRPCIndex"`
}

var _ = poollist.RegisterFactoryCE(DexType, NewPoolsListUpdater)

func NewPoolsListUpdater(config *Config, ethrpcClient *ethrpc.Client) *PoolsListUpdater {
	return &PoolsListUpdater{
		config:       *config,
		ethrpcClient: ethrpcClient,
	}
}

func (u *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	logger.WithFields(logger.Fields{
		"dexType": DexType,
	}).Infof("Start updating pools list ...")
	defer func() {
		logger.WithFields(logger.Fields{
			"dexType": DexType,
		}).Infof("Finish updating pools list.")
	}()

	var metadata Metadata
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, nil, err
		}
	}

	// Phase 1: static pool list (RPC path)
	var staticPools []entity.Pool
	nextRPCIndex := metadata.LastProcessedRPCIndex
	if u.config.AllowRPCFetch && nextRPCIndex < len(u.config.StaticPoolList) {
		var err error
		staticPools, nextRPCIndex, err = u.getPoolsFromStaticList(ctx, metadata.LastProcessedRPCIndex)
		if err != nil {
			return nil, nil, err
		}
	}

	// Phase 2: resolver discovery path (existing)
	allPoolsFromResolver, err := u.getAllPools(ctx)
	if err != nil {
		return nil, nil, err
	}

	newMetadataBytes, err := json.Marshal(Metadata{
		LastSyncPoolsLength:   len(allPoolsFromResolver),
		LastProcessedRPCIndex: nextRPCIndex,
	})
	if err != nil {
		return nil, nil, err
	}

	// only handle new pools since last sync
	newResolverPools := allPoolsFromResolver
	if metadata.LastSyncPoolsLength > 0 {
		newResolverPools = allPoolsFromResolver[metadata.LastSyncPoolsLength:]
	}

	resolverPools := make([]entity.Pool, 0, len(newResolverPools))
	for _, curPool := range newResolverPools {
		pool, err := u.buildPool(ctx, curPool)
		if err != nil {
			return nil, nil, err
		}
		resolverPools = append(resolverPools, pool)
	}

	merged := deduplicatePools(staticPools, resolverPools)

	return merged, newMetadataBytes, nil
}

// getPoolsFromStaticList fetches the next batch of pools from config.StaticPoolList.
// It calls getPoolReservesAdjusted for each address in one batched RPC call, then fetches
// token decimals for all unique tokens. Returns built pools and the next index to process.
func (u *PoolsListUpdater) getPoolsFromStaticList(ctx context.Context, fromIndex int) ([]entity.Pool, int, error) {
	toIndex := min(fromIndex+staticPoolBatchSize, len(u.config.StaticPoolList))

	batch := u.config.StaticPoolList[fromIndex:toIndex]

	poolReservesResults := make([]*PoolWithReserves, len(batch))
	for i := range poolReservesResults {
		poolReservesResults[i] = &PoolWithReserves{}
	}

	req := u.ethrpcClient.R().SetContext(ctx)
	for i, addr := range batch {
		req.AddCall(&ethrpc.Call{
			ABI:    dexReservesResolverABI,
			Target: u.config.DexReservesResolver,
			Method: DRRMethodGetPoolReservesAdjusted,
			Params: []any{common.HexToAddress(addr)},
		}, []any{&poolReservesResults[i]})
	}

	if _, err := req.TryAggregate(); err != nil {
		return nil, fromIndex, err
	}

	// collect unique non-native token addresses for batch decimals fetch
	zeroAddr := common.Address{}
	tokenSet := make(map[common.Address]struct{})
	valid := make([]bool, len(batch))

	for i, p := range poolReservesResults {
		if p == nil || p.PoolAddress == zeroAddr {
			logger.WithFields(logger.Fields{
				"dexType": DexType,
				"pool":    batch[i],
			}).Warn("[getPoolsFromStaticList] pool returned zero address, skipping")
			continue
		}
		valid[i] = true
		if !strings.EqualFold(p.Token0Address.Hex(), valueobject.NativeAddress) {
			tokenSet[p.Token0Address] = struct{}{}
		}
		if !strings.EqualFold(p.Token1Address.Hex(), valueobject.NativeAddress) {
			tokenSet[p.Token1Address] = struct{}{}
		}
	}

	uniqueTokens := make([]common.Address, 0, len(tokenSet))
	for addr := range tokenSet {
		uniqueTokens = append(uniqueTokens, addr)
	}

	decimalsMap := u.fetchTokenDecimalsMap(ctx, uniqueTokens)

	pools := make([]entity.Pool, 0, len(batch))
	for i, p := range poolReservesResults {
		if !valid[i] {
			continue
		}

		token0Decimals := getDecimals(p.Token0Address, decimalsMap)
		token1Decimals := getDecimals(p.Token1Address, decimalsMap)

		pool, err := buildPoolEntity(p, token0Decimals, token1Decimals, u.config)
		if err != nil {
			logger.WithFields(logger.Fields{
				"dexType": DexType,
				"pool":    batch[i],
				"error":   err,
			}).Warn("[getPoolsFromStaticList] failed to build pool, skipping")
			continue
		}

		pools = append(pools, pool)
	}

	logger.WithFields(logger.Fields{
		"dexType": DexType,
		"total":   len(batch),
		"built":   len(pools),
	}).Info("[getPoolsFromStaticList] batch complete")

	return pools, toIndex, nil
}

// fetchTokenDecimalsMap batch-fetches ERC20 decimals for all given addresses.
// Falls back to defaultTokenDecimals (18) on failure — never returns an error.
func (u *PoolsListUpdater) fetchTokenDecimalsMap(ctx context.Context, addresses []common.Address) map[common.Address]uint8 {
	result := make(map[common.Address]uint8, len(addresses))
	if len(addresses) == 0 {
		return result
	}

	decimals := make([]uint8, len(addresses))
	req := u.ethrpcClient.R().SetContext(ctx)
	for i, addr := range addresses {
		req.AddCall(&ethrpc.Call{
			ABI:    erc20,
			Target: addr.Hex(),
			Method: TokenMethodDecimals,
		}, []any{&decimals[i]})
	}

	if _, err := req.TryAggregate(); err != nil {
		logger.WithFields(logger.Fields{
			"dexType": DexType,
			"error":   err,
		}).Warn("[fetchTokenDecimalsMap] batch failed, using defaultTokenDecimals for all tokens")
		for _, addr := range addresses {
			result[addr] = defaultTokenDecimals
		}
		return result
	}

	for i, addr := range addresses {
		result[addr] = decimals[i]
	}

	return result
}

// buildPool fetches token decimals and builds an entity.Pool from a PoolWithReserves.
// Used by the resolver discovery path which processes one pool at a time.
func (u *PoolsListUpdater) buildPool(ctx context.Context, curPool PoolWithReserves) (entity.Pool, error) {
	token0Decimals, token1Decimals, err := u.readTokensDecimals(ctx, curPool.Token0Address, curPool.Token1Address)
	if err != nil {
		return entity.Pool{}, err
	}
	return buildPoolEntity(&curPool, token0Decimals, token1Decimals, u.config)
}

// buildPoolEntity constructs an entity.Pool from resolved pool data and token decimals.
func buildPoolEntity(curPool *PoolWithReserves, token0Decimals, token1Decimals uint8, cfg Config) (entity.Pool, error) {
	staticExtraBytes, err := json.Marshal(&StaticExtra{
		DexReservesResolver: cfg.DexReservesResolver,
		HasNative: strings.EqualFold(curPool.Token0Address.Hex(), valueobject.NativeAddress) ||
			strings.EqualFold(curPool.Token1Address.Hex(), valueobject.NativeAddress),
	})
	if err != nil {
		return entity.Pool{}, err
	}

	extraBytes, err := json.Marshal(PoolExtra{
		CollateralReserves: curPool.CollateralReserves,
		DebtReserves:       curPool.DebtReserves,
		DexLimits:          curPool.Limits,
		CenterPrice:        curPool.CenterPrice,
	})
	if err != nil {
		return entity.Pool{}, err
	}

	return entity.Pool{
		Address:  hexutil.Encode(curPool.PoolAddress[:]),
		Exchange: valueobject.ExchangeFluidDexT1,
		Type:     DexType,
		Reserves: entity.PoolReserves{
			getMaxReserves(
				token0Decimals,
				curPool.Limits.WithdrawableToken0,
				curPool.Limits.BorrowableToken0,
				curPool.CollateralReserves.Token0RealReserves,
				curPool.DebtReserves.Token0RealReserves).String(),
			getMaxReserves(
				token1Decimals,
				curPool.Limits.WithdrawableToken1,
				curPool.Limits.BorrowableToken1,
				curPool.CollateralReserves.Token1RealReserves,
				curPool.DebtReserves.Token1RealReserves).String(),
		},
		Tokens: []*entity.PoolToken{
			{
				Address:   valueobject.WrapNativeLower(curPool.Token0Address.Hex(), cfg.ChainID),
				Swappable: true,
				Decimals:  token0Decimals,
			},
			{
				Address:   valueobject.WrapNativeLower(curPool.Token1Address.Hex(), cfg.ChainID),
				Swappable: true,
				Decimals:  token1Decimals,
			},
		},
		SwapFee:     float64(curPool.Fee.Int64()) / FeePercentPrecision,
		Extra:       string(extraBytes),
		StaticExtra: string(staticExtraBytes),
	}, nil
}

// getDecimals returns the decimals for a token address, defaulting to 18 for the native token.
func getDecimals(addr common.Address, decimalsMap map[common.Address]uint8) uint8 {
	if strings.EqualFold(addr.Hex(), valueobject.NativeAddress) {
		return 18
	}
	if d, ok := decimalsMap[addr]; ok {
		return d
	}
	return defaultTokenDecimals
}

// deduplicatePools merges priority and fallback pools, with priority pools winning on address collision.
func deduplicatePools(priority, fallback []entity.Pool) []entity.Pool {
	seen := make(map[string]struct{}, len(priority))
	result := make([]entity.Pool, 0, len(priority)+len(fallback))

	for _, p := range priority {
		key := strings.ToLower(p.Address)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			result = append(result, p)
		}
	}

	for _, p := range fallback {
		key := strings.ToLower(p.Address)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			result = append(result, p)
		}
	}

	return result
}

func (u *PoolsListUpdater) getAllPools(ctx context.Context) ([]PoolWithReserves, error) {
	var pools []PoolWithReserves

	req := u.ethrpcClient.R().SetContext(ctx)

	req.AddCall(&ethrpc.Call{
		ABI:    dexReservesResolverABI,
		Target: u.config.DexReservesResolver,
		Method: DRRMethodGetAllPoolsReservesAdjusted,
	}, []any{&pools})

	if _, err := req.Aggregate(); err != nil {
		logger.WithFields(logger.Fields{
			"dexType": DexType,
			"error":   err,
		}).Error("Failed to get all pools reserves")
		return nil, err
	}

	return pools, nil
}

func (u *PoolsListUpdater) readTokensDecimals(ctx context.Context, token0 common.Address, token1 common.Address) (uint8, uint8, error) {
	var decimals0, decimals1 uint8

	req := u.ethrpcClient.R().SetContext(ctx)

	if strings.EqualFold(valueobject.NativeAddress, token0.String()) {
		decimals0 = 18
	} else {
		req.AddCall(&ethrpc.Call{
			ABI:    erc20,
			Target: token0.String(),
			Method: TokenMethodDecimals,
			Params: nil,
		}, []any{&decimals0})
	}

	if strings.EqualFold(valueobject.NativeAddress, token1.String()) {
		decimals1 = 18
	} else {
		req.AddCall(&ethrpc.Call{
			ABI:    erc20,
			Target: token1.String(),
			Method: TokenMethodDecimals,
			Params: nil,
		}, []any{&decimals1})
	}

	_, err := req.Aggregate()
	if err != nil {
		logger.WithFields(logger.Fields{
			"dexType": DexType,
			"error":   err,
		}).Error("can not read token info")
		return 0, 0, err
	}

	return decimals0, decimals1, nil
}
