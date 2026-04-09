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

type PoolsListUpdater struct {
	config       Config
	ethrpcClient *ethrpc.Client
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

	// Phase 1: fetch pools from the pre-configured static list via individual RPC calls.
	var staticPools []entity.Pool
	nextStaticIndex := metadata.LastProcessedStaticIndex
	if len(u.config.StaticPoolList) > 0 {
		batchSize := u.config.StaticPoolBatchSize
		if batchSize <= 0 {
			batchSize = defaultStaticPoolBatchSize
		}
		var err error
		staticPools, nextStaticIndex, err = u.getPoolsFromStaticList(ctx, metadata.LastProcessedStaticIndex, batchSize)
		if err != nil {
			return nil, nil, err
		}
	}

	// Phase 2: bulk-fetch all pools from the resolver and process only newly-seen ones.
	allPools, err := u.getAllPools(ctx)
	if err != nil {
		return nil, nil, err
	}

	newLastSyncLength := len(allPools)
	if metadata.LastSyncPoolsLength > 0 {
		allPools = allPools[metadata.LastSyncPoolsLength:]
	}

	dynamicPools := make([]entity.Pool, 0, len(allPools))
	for _, curPool := range allPools {
		token0Decimals, token1Decimals, err := u.readTokensDecimals(ctx, curPool.Token0Address, curPool.Token1Address)
		if err != nil {
			return nil, nil, err
		}
		pool, err := buildPool(curPool, token0Decimals, token1Decimals, u.config.ChainID, u.config.DexReservesResolver)
		if err != nil {
			return nil, nil, err
		}
		dynamicPools = append(dynamicPools, pool)
	}

	pools := deduplicatePools(staticPools, dynamicPools)

	newMetadataBytes, err := json.Marshal(Metadata{
		LastSyncPoolsLength:      newLastSyncLength,
		LastProcessedStaticIndex: nextStaticIndex,
	})
	if err != nil {
		return nil, nil, err
	}

	return pools, newMetadataBytes, nil
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

// buildPool constructs an entity.Pool from a PoolWithReserves and token decimals.
// Used by both the static-list phase and the bulk-resolver phase.
func buildPool(curPool PoolWithReserves, token0Decimals, token1Decimals uint8, chainID valueobject.ChainID, dexReservesResolver string) (entity.Pool, error) {
	staticExtraBytes, err := json.Marshal(&StaticExtra{
		DexReservesResolver: dexReservesResolver,
		HasNative: strings.EqualFold(curPool.Token0Address.Hex(), valueobject.NativeAddress) ||
			strings.EqualFold(curPool.Token1Address.Hex(), valueobject.NativeAddress),
	})
	if err != nil {
		return entity.Pool{}, err
	}

	extra := PoolExtra{
		CollateralReserves: curPool.CollateralReserves,
		DebtReserves:       curPool.DebtReserves,
		DexLimits:          curPool.Limits,
		CenterPrice:        curPool.CenterPrice,
	}

	extraBytes, err := json.Marshal(extra)
	if err != nil {
		logger.WithFields(logger.Fields{"dexType": DexType, "error": err}).Error("Error marshaling extra data")
		return entity.Pool{}, err
	}

	pool := entity.Pool{
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
				Address:   valueobject.WrapNativeLower(curPool.Token0Address.Hex(), chainID),
				Swappable: true,
				Decimals:  token0Decimals,
			},
			{
				Address:   valueobject.WrapNativeLower(curPool.Token1Address.Hex(), chainID),
				Swappable: true,
				Decimals:  token1Decimals,
			},
		},
		SwapFee:     float64(curPool.Fee.Int64()) / FeePercentPrecision,
		Extra:       string(extraBytes),
		StaticExtra: string(staticExtraBytes),
	}

	return pool, nil
}

// deduplicatePools merges two pool slices, keeping the first occurrence of each address.
// primary pools take priority over secondary.
func deduplicatePools(primary, secondary []entity.Pool) []entity.Pool {
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	result := make([]entity.Pool, 0, len(primary)+len(secondary))
	for _, p := range append(primary, secondary...) {
		key := strings.ToLower(p.Address)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			result = append(result, p)
		}
	}
	return result
}
