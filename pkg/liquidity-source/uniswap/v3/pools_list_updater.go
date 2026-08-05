package uniswapv3

import (
	"context"
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

// getPoolsList fetches pools from the subgraph ordered by createdAtTimestamp,
// starting at lastCreatedAtTimestamp (inclusive). Used for timestamp-cursor pagination.
func (d *PoolsListUpdater) getPoolsList(ctx context.Context, lastCreatedAtTimestamp *big.Int, first, skip int) ([]SubgraphPool, error) {
	allowSubgraphError := d.config.IsAllowSubgraphError()
	req := graphqlpkg.NewRequest(getPoolsListQuery(allowSubgraphError, lastCreatedAtTimestamp, first, skip))

	var response struct {
		Pools []SubgraphPool `json:"pools"`
	}

	if err := d.graphqlClient.Run(ctx, req, &response); err != nil {
		// Workaround for broken subgraphs (e.g. Arbitrum) that return partial data with an error
		if allowSubgraphError && len(response.Pools) > 0 {
			return response.Pools, nil
		}
		logger.WithFields(logger.Fields{
			"dexId": d.config.DexID,
			"error": err,
		}).Errorf("[getPoolsList] subgraph query failed")
		return nil, err
	}

	return response.Pools, nil
}

// getPoolsByAddresses queries the subgraph for a specific set of pool addresses using id_in.
// Used as a non-fatal fallback when RPC metadata fetch fails for individual pools.
func (d *PoolsListUpdater) getPoolsByAddresses(ctx context.Context, addresses []string) ([]SubgraphPool, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	req := graphqlpkg.NewRequest(getPoolsByAddressesQuery(addresses))

	var response struct {
		Pools []SubgraphPool `json:"pools"`
	}

	if err := d.graphqlClient.Run(ctx, req, &response); err != nil {
		logger.WithFields(logger.Fields{
			"dexId": d.config.DexID,
			"count": len(addresses),
			"error": err,
		}).Warn("[getPoolsByAddresses] subgraph fallback query failed")
		return nil, err
	}

	return response.Pools, nil
}

// buildPoolFromSubgraphData converts a SubgraphPool to entity.Pool.
// Decimals that fail to parse fall back to defaultTokenDecimals (18).
func (d *PoolsListUpdater) buildPoolFromSubgraphData(p SubgraphPool, tickSpacings map[string]uint64) entity.Pool {
	token0Decimals, err := kutils.Atou[uint8](p.Token0.Decimals)
	if err != nil {
		token0Decimals = defaultTokenDecimals
	}
	token1Decimals, err := kutils.Atou[uint8](p.Token1.Decimals)
	if err != nil {
		token1Decimals = defaultTokenDecimals
	}

	swapFee, _ := strconv.ParseFloat(p.FeeTier, 64)
	createdAtTimestamp, _ := kutils.Atoi[int64](p.CreatedAtTimestamp)

	extraBytes, _ := json.Marshal(Extra{TickSpacing: tickSpacings[p.ID]})
	staticBytes, _ := json.Marshal(StaticExtra{PoolId: p.ID})

	return entity.Pool{
		Address:   strings.ToLower(p.ID),
		SwapFee:   swapFee,
		Exchange:  d.config.DexID,
		Type:      DexTypeUniswapV3,
		Timestamp: createdAtTimestamp,
		Reserves:  entity.PoolReserves{"0", "0"},
		Tokens: []*entity.PoolToken{
			{Address: strings.ToLower(p.Token0.Address), Symbol: p.Token0.Symbol, Decimals: token0Decimals, Swappable: true},
			{Address: strings.ToLower(p.Token1.Address), Symbol: p.Token1.Symbol, Decimals: token1Decimals, Swappable: true},
		},
		Extra:       string(extraBytes),
		StaticExtra: string(staticBytes),
	}
}

func (d *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	// Initialize cursor at zero for first run (subgraph query uses createdAtTimestamp_gte)
	metadata := Metadata{LastCreatedAtTimestamp: integer.Zero()}
	if len(metadataBytes) != 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, metadataBytes, err
		}
		// Guard: old metadata format had no LastCreatedAtTimestamp; initialize on upgrade
		if metadata.LastCreatedAtTimestamp == nil {
			metadata.LastCreatedAtTimestamp = integer.Zero()
		}
	}

	var rpcPools, subgraphPools []entity.Pool

	// Phase 1: Initialize as many pools as possible from config.StaticPoolList via RPC.
	// Any pool whose RPC metadata is invalid (zero addresses, nil fee/tickSpacing) is
	// collected in rpcFailedAddresses for subgraph fallback in Phase 2.
	var rpcFailedAddresses []string
	if d.config.AllowRPCFetch {
		logger.WithFields(logger.Fields{
			"dexId":     d.config.DexID,
			"fromIndex": metadata.LastProcessedRPCIndex,
		}).Info("[GetNewPools] starting RPC phase")

		var nextIndex int
		var err error
		rpcPools, rpcFailedAddresses, nextIndex, err = d.getPoolsFromRPC(ctx, metadata.LastProcessedRPCIndex, rpcPoolBatchSize)
		if err != nil {
			return nil, metadataBytes, err
		}
		metadata.LastProcessedRPCIndex = nextIndex
	}

	// Phase 2: Subgraph discovery — timestamp-cursor pagination plus fallback for RPC failures.
	if d.config.AllowSubgraphFetch {
		// 2a: Regular discovery using the LastCreatedAtTimestamp cursor
		logger.WithFields(logger.Fields{
			"dexId":  d.config.DexID,
			"fromTs": metadata.LastCreatedAtTimestamp,
		}).Info("[GetNewPools] starting subgraph phase")

		sgTimestampPools, err := d.getPoolsList(ctx, metadata.LastCreatedAtTimestamp, graphFirstLimit, 0)
		if err != nil {
			// Non-fatal: RPC pools collected in Phase 1 are preserved; subgraph phase is skipped.
			logger.WithFields(logger.Fields{
				"dexId":    d.config.DexID,
				"rpcPools": len(rpcPools),
				"error":    err,
			}).Warn("[GetNewPools] subgraph phase failed, continuing with RPC pools only")
		} else {
			// Advance cursor based on timestamp-ordered results only
			if len(sgTimestampPools) > 0 {
				last := sgTimestampPools[len(sgTimestampPools)-1]
				if ts, ok := new(big.Int).SetString(last.CreatedAtTimestamp, 10); ok {
					metadata.LastCreatedAtTimestamp = ts
				} else {
					logger.WithFields(logger.Fields{
						"dexId": d.config.DexID,
						"pool":  last.ID,
						"ts":    last.CreatedAtTimestamp,
					}).Warn("[GetNewPools] could not parse last pool timestamp, cursor not advanced")
				}
			}

			// 2b: Subgraph fallback for pools that failed RPC metadata fetch
			allSgPools := sgTimestampPools
			if len(rpcFailedAddresses) > 0 {
				logger.WithFields(logger.Fields{
					"dexId": d.config.DexID,
					"count": len(rpcFailedAddresses),
				}).Info("[GetNewPools] attempting subgraph fallback for RPC-failed pools")

				fallbackPools, fallbackErr := d.getPoolsByAddresses(ctx, rpcFailedAddresses)
				if fallbackErr != nil {
					// Non-fatal: log and continue without fallback pools
					logger.WithFields(logger.Fields{
						"dexId": d.config.DexID,
						"error": fallbackErr,
					}).Warn("[GetNewPools] subgraph fallback failed, skipping affected pools")
				} else {
					logger.WithFields(logger.Fields{
						"dexId":     d.config.DexID,
						"attempted": len(rpcFailedAddresses),
						"recovered": len(fallbackPools),
					}).Info("[GetNewPools] subgraph fallback complete")
					allSgPools = append(allSgPools, fallbackPools...)
				}
			}

			// Fetch tickSpacings for all subgraph pools in one batched RPC call
			tickSpacings, _ := FetchTickSpacings(
				ctx,
				lo.Map(allSgPools, func(p SubgraphPool, _ int) string { return p.ID }),
				d.ethrpcClient,
				abis.UniswapV3PoolABI,
				methodTickSpacing,
			)

			for _, p := range allSgPools {
				if p.Token0.Address == "" || p.Token1.Address == "" {
					logger.WithFields(logger.Fields{
						"dexId": d.config.DexID,
						"pool":  p.ID,
					}).Warn("[GetNewPools] skipping subgraph pool with empty token address")
					continue
				}
				subgraphPools = append(subgraphPools, d.buildPoolFromSubgraphData(p, tickSpacings))
			}

			logger.WithFields(logger.Fields{
				"dexId":         d.config.DexID,
				"timestampPool": len(sgTimestampPools),
				"subgraphPools": len(subgraphPools),
			}).Info("[GetNewPools] subgraph phase complete")
		}
	}

	// Merge: RPC pools take priority; deduplicatePools keeps first occurrence per address.
	pools := deduplicatePools(rpcPools, subgraphPools)

	newMetadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, metadataBytes, err
	}

	logger.WithFields(logger.Fields{
		"dexId":      d.config.DexID,
		"total":      len(pools),
		"rpcPools":   len(rpcPools),
		"sgPools":    len(subgraphPools),
		"duplicates": len(rpcPools) + len(subgraphPools) - len(pools),
	}).Info("[GetNewPools] finished")

	return pools, newMetadataBytes, nil
}

// deduplicatePools merges pool slices, keeping the first occurrence of each address.
// Callers should pass higher-priority slices first.
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
