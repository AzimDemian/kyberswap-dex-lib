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

type Metadata struct {
	LastSyncPoolsLength int `json:"lastSyncPoolsLength"`
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

	allPools, err := u.getAllPools(ctx)
	if err != nil {
		return nil, nil, err
	}

	newMetadataBytes, err := json.Marshal(Metadata{
		LastSyncPoolsLength: len(allPools),
	})
	if err != nil {
		return nil, nil, err
	}

	var metadata Metadata
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, nil, err
		}
	}

	if metadata.LastSyncPoolsLength > 0 {
		// only handle new pools after last synced index
		allPools = allPools[metadata.LastSyncPoolsLength:]
	}

	decimalsMap := u.readAllTokensDecimals(ctx, allPools)

	pools := make([]entity.Pool, 0, len(allPools))

	for _, curPool := range allPools {
		token0Decimals, ok0 := decimalsMap[curPool.Token0Address]
		token1Decimals, ok1 := decimalsMap[curPool.Token1Address]
		if !ok0 || !ok1 {
			logger.WithFields(logger.Fields{
				"dexType": DexType,
				"pool":    curPool.PoolAddress.Hex(),
				"token0":  curPool.Token0Address.Hex(),
				"token1":  curPool.Token1Address.Hex(),
			}).Warn("skipping pool: could not fetch decimals for one or more tokens")
			continue
		}

		staticExtraBytes, err := json.Marshal(&StaticExtra{
			DexReservesResolver: u.config.DexReservesResolver,
			HasNative: strings.EqualFold(curPool.Token0Address.Hex(), valueobject.NativeAddress) ||
				strings.EqualFold(curPool.Token1Address.Hex(), valueobject.NativeAddress),
		})
		if err != nil {
			return nil, nil, err
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
			return nil, nil, err
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
					Address:   valueobject.WrapNativeLower(curPool.Token0Address.Hex(), u.config.ChainID),
					Swappable: true,
					Decimals:  token0Decimals,
				},
				{
					Address:   valueobject.WrapNativeLower(curPool.Token1Address.Hex(), u.config.ChainID),
					Swappable: true,
					Decimals:  token1Decimals,
				},
			},
			SwapFee:     float64(curPool.Fee.Int64()) / FeePercentPrecision,
			Extra:       string(extraBytes),
			StaticExtra: string(staticExtraBytes),
		}

		pools = append(pools, pool)
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

// readAllTokensDecimals fetches ERC20 decimals for all unique token addresses across all
// pools in one batched TryAggregate call. Tokens whose decimals() call fails are omitted
// from the returned map; callers must skip pools with missing entries.
func (u *PoolsListUpdater) readAllTokensDecimals(ctx context.Context, pools []PoolWithReserves) map[common.Address]uint8 {
	decimalsMap := make(map[common.Address]uint8)
	var uniqueTokens []common.Address
	seen := make(map[common.Address]bool)

	for _, pool := range pools {
		for _, addr := range []common.Address{pool.Token0Address, pool.Token1Address} {
			if strings.EqualFold(addr.Hex(), valueobject.NativeAddress) {
				decimalsMap[addr] = 18
			} else if !seen[addr] {
				seen[addr] = true
				uniqueTokens = append(uniqueTokens, addr)
			}
		}
	}

	if len(uniqueTokens) == 0 {
		return decimalsMap
	}

	decimalsResults := make([]uint8, len(uniqueTokens))
	req := u.ethrpcClient.R().SetContext(ctx)
	for i, addr := range uniqueTokens {
		req.AddCall(&ethrpc.Call{
			ABI:    erc20,
			Target: addr.String(),
			Method: TokenMethodDecimals,
		}, []any{&decimalsResults[i]})
	}

	resp, err := req.TryAggregate()
	if err != nil {
		logger.WithFields(logger.Fields{"dexType": DexType, "error": err}).
			Error("token decimals multicall failed")
		return decimalsMap
	}

	for i, addr := range uniqueTokens {
		if !resp.Result[i] {
			logger.WithFields(logger.Fields{"dexType": DexType, "token": addr.Hex()}).
				Warn("decimals() reverted for token, skipping")
			continue
		}
		decimalsMap[addr] = decimalsResults[i]
	}

	return decimalsMap
}
