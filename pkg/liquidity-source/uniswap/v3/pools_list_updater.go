package uniswapv3

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/KyberNetwork/blockchain-toolkit/integer"
	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/kutils"
	"github.com/KyberNetwork/logger"
	"github.com/goccy/go-json"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3/abis"
	poollist "github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool/list"
	graphqlpkg "github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/graphql"
)

type PoolsListUpdater struct {
	config        *Config
	ethrpcClient  *ethrpc.Client
	graphqlClient *graphqlpkg.Client
}

var _ = poollist.RegisterFactoryCEG(DexTypeUniswapV3, NewPoolsListUpdater)

func NewPoolsListUpdater(
	cfg *Config,
	ethrpcClient *ethrpc.Client,
	graphqlClient *graphqlpkg.Client,
) *PoolsListUpdater {
	return &PoolsListUpdater{
		config:        cfg,
		ethrpcClient:  ethrpcClient,
		graphqlClient: graphqlClient,
	}
}

func (d *PoolsListUpdater) getPoolsList(ctx context.Context, lastCreatedAtTimestamp *big.Int, first, skip int) ([]SubgraphPool, error) {
	allowSubgraphError := d.config.IsAllowSubgraphError()

	req := graphqlpkg.NewRequest(getPoolsListQuery(allowSubgraphError, lastCreatedAtTimestamp, first, skip))

	var response struct {
		Pools []SubgraphPool `json:"pools"`
	}

	if err := d.graphqlClient.Run(ctx, req, &response); err != nil {
		// Workaround at the moment to live with the error subgraph on Arbitrum
		if allowSubgraphError && len(response.Pools) > 0 {
			return response.Pools, nil
		}

		logger.WithFields(logger.Fields{
			"error": err,
		}).Errorf("failed to query subgraph")
		return nil, err
	}

	return response.Pools, nil
}

func (d *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	metadata := Metadata{
		LastCreatedAtTimestamp: integer.Zero(),
	}
	if len(metadataBytes) != 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, metadataBytes, err
		}
	}

	var subgraphPools, rpcPools []entity.Pool

	if d.config.AllowSubgraphFetch {
		sgPools, err := d.getPoolsList(ctx, metadata.LastCreatedAtTimestamp, graphFirstLimit, 0)
		if err != nil {
			logger.WithFields(logger.Fields{
				"error": err,
			}).Errorf("failed to get pools list from subgraph")
			return nil, metadataBytes, err
		}

		logger.Infof("got %v subgraph pools from %s subgraph", len(sgPools), d.config.DexID)

		tickSpacings, _ := FetchTickSpacings(
			ctx,
			lo.Map(sgPools, func(item SubgraphPool, _ int) string { return item.ID }),
			d.ethrpcClient,
			abis.UniswapV3PoolABI,
			methodTickSpacing,
		)

		for _, p := range sgPools {
			tokens := make([]*entity.PoolToken, 0, 2)
			reserves := make([]string, 0, 2)

			extraField := Extra{TickSpacing: tickSpacings[p.ID]}
			staticField := StaticExtra{PoolId: p.ID}

			if p.Token0.Address != "" {
				token0Decimals, err := kutils.Atou[uint8](p.Token0.Decimals)
				if err != nil {
					token0Decimals = defaultTokenDecimals
				}
				tokens = append(tokens, &entity.PoolToken{
					Address: p.Token0.Address, Symbol: p.Token0.Symbol,
					Decimals: token0Decimals, Swappable: true,
				})
				reserves = append(reserves, "0")
			}

			if p.Token1.Address != "" {
				token1Decimals, err := kutils.Atou[uint8](p.Token1.Decimals)
				if err != nil {
					token1Decimals = defaultTokenDecimals
				}
				tokens = append(tokens, &entity.PoolToken{
					Address: p.Token1.Address, Symbol: p.Token1.Symbol,
					Decimals: token1Decimals, Swappable: true,
				})
				reserves = append(reserves, "0")
			}

			swapFee, _ := strconv.ParseFloat(p.FeeTier, 64)
			createdAtTimestamp, err := kutils.Atoi[int64](p.CreatedAtTimestamp)
			if err != nil {
				return nil, metadataBytes, fmt.Errorf("invalid CreatedAtTimestamp: %v, pool: %v", p.CreatedAtTimestamp, p.ID)
			}

			extraBytes, _ := json.Marshal(extraField)
			staticBytes, _ := json.Marshal(staticField)
			subgraphPools = append(subgraphPools, entity.Pool{
				Address:     p.ID,
				SwapFee:     swapFee,
				Exchange:    d.config.DexID,
				Type:        DexTypeUniswapV3,
				Timestamp:   createdAtTimestamp,
				Reserves:    reserves,
				Tokens:      tokens,
				Extra:       string(extraBytes),
				StaticExtra: string(staticBytes),
			})
		}

		// Update subgraph metadata
		if len(sgPools) > 0 {
			last := sgPools[len(sgPools)-1]
			ts, ok := new(big.Int).SetString(last.CreatedAtTimestamp, 10)
			if !ok {
				return nil, metadataBytes, fmt.Errorf("invalid CreatedAtTimestamp: %v, pool: %v", last.CreatedAtTimestamp, last.ID)
			}
			metadata.LastCreatedAtTimestamp = ts
		}
	}

	if d.config.AllowRPCFetch {
		var (
			nextIndex int
			err       error
		)
		rpcPools, nextIndex, err = d.getPoolsFromRPC(ctx, metadata.LastProcessedRPCIndex, graphFirstLimit)
		if err != nil {
			return nil, metadataBytes, err
		}
		metadata.LastProcessedRPCIndex = nextIndex
	}

	pools := deduplicatePools(subgraphPools, rpcPools)

	newMetadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, metadataBytes, err
	}

	logger.WithFields(logger.Fields{
		"dexId":         d.config.DexID,
		"pools":         len(pools),
		"subgraphPools": len(subgraphPools),
		"rpcPools":      len(rpcPools),
	}).Info("finished getting new pools")

	return pools, newMetadataBytes, nil
}

// deduplicatePools merges pool slices, keeping the first occurrence of each address.
func deduplicatePools(sources ...[]entity.Pool) []entity.Pool {
	seen := make(map[string]struct{})
	var result []entity.Pool
	for _, pools := range sources {
		for _, p := range pools {
			addr := strings.ToLower(p.Address)
			if _, exists := seen[addr]; !exists {
				seen[addr] = struct{}{}
				result = append(result, p)
			}
		}
	}
	return result
}
