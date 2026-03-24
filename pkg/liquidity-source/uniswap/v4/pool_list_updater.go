package uniswapv4

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/kutils"
	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	poollist "github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool/list"
	graphqlpkg "github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/graphql"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

type (
	PoolsListUpdater struct {
		config        *Config
		ethrpcClient  *ethrpc.Client
		graphqlClient *graphqlpkg.Client
	}

	Metadata struct {
		// Subgraph mode fields (unchanged for backward compatibility)
		LastCreatedAtTimestamp int    `json:"lastCreatedAtTimestamp"`
		LastProcessedPoolId    string `json:"lastProcessedPoolID"`

		// RPC mode: index into the pools list (list_of_pools.json)
		LastProcessedRPCIndex int `json:"lastProcessedRPCIndex"`
	}
)

var _ = poollist.RegisterFactoryCEG(DexType, NewPoolListUpdater)

func NewPoolListUpdater(
	config *Config,
	ethrpcClient *ethrpc.Client,
	graphqlClient *graphqlpkg.Client,
) *PoolsListUpdater {
	return &PoolsListUpdater{
		config:        config,
		ethrpcClient:  ethrpcClient,
		graphqlClient: graphqlClient,
	}
}

func (u *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	var metadata Metadata
	if len(metadataBytes) != 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, metadataBytes, err
		}
	}

	var subgraphPools, rpcPools []entity.Pool

	if u.config.EnableSubgraphUpdater {
		var err error
		subgraphPools, err = u.getPoolsFromSubgraph(ctx, &metadata)
		if err != nil {
			return nil, metadataBytes, err
		}
	}

	if u.config.EnableRPCUpdater {
		var (
			nextIndex int
			err       error
		)
		rpcPools, nextIndex, err = u.getPoolsFromRPC(ctx, metadata.LastProcessedRPCIndex, u.config.NewPoolLimit)
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
		"dexId":         u.config.DexID,
		"pools":         len(pools),
		"subgraphPools": len(subgraphPools),
		"rpcPools":      len(rpcPools),
	}).Info("finished getting new pools")

	return pools, newMetadataBytes, nil
}

// getPoolsFromSubgraph fetches new pools from the subgraph and updates the relevant metadata fields.
func (u *PoolsListUpdater) getPoolsFromSubgraph(ctx context.Context, metadata *Metadata) ([]entity.Pool, error) {
	subgraphPools, err := u.getPoolsList(ctx, metadata.LastCreatedAtTimestamp, u.config.NewPoolLimit)
	if err != nil {
		return nil, err
	}

	pools := make([]entity.Pool, 0, len(subgraphPools))
	chainID := valueobject.ChainID(u.config.ChainID)

	for _, p := range subgraphPools {
		token0Decimals, err := kutils.Atou[uint8](p.Token0.Decimals)
		if err != nil {
			return nil, err
		}
		token1Decimals, err := kutils.Atou[uint8](p.Token1.Decimals)
		if err != nil {
			return nil, err
		}
		tokens := []*entity.PoolToken{
			{Address: p.Token0.ID, Decimals: token0Decimals, Swappable: true},
			{Address: p.Token1.ID, Decimals: token1Decimals, Swappable: true},
		}
		for idx, token := range tokens {
			if token.Address == EmptyAddress {
				tokens[idx].Address = strings.ToLower(valueobject.WrappedNativeMap[chainID])
			}
		}

		tickSpacing, err := kutils.Atoi[int32](p.TickSpacing)
		if err != nil {
			return nil, err
		}
		fee, err := kutils.Atou[uint32](p.Fee)
		if err != nil {
			return nil, err
		}

		staticExtra := StaticExtra{
			IsNative:               [2]bool{p.Token0.ID == EmptyAddress, p.Token1.ID == EmptyAddress},
			Fee:                    fee,
			TickSpacing:            tickSpacing,
			HooksAddress:           common.HexToAddress(p.Hooks),
			UniversalRouterAddress: common.HexToAddress(u.config.UniversalRouterAddress),
			Permit2Address:         common.HexToAddress(u.config.Permit2Address),
			Multicall3Address:      common.HexToAddress(u.config.Multicall3Address),
		}

		staticExtraBytes, err := json.Marshal(staticExtra)
		if err != nil {
			return nil, err
		}

		hook, _ := GetHook(staticExtra.HooksAddress, &HookParam{Cfg: u.config})
		pool := entity.Pool{
			Address:     p.ID,
			SwapFee:     float64(fee),
			Exchange:    hook.GetExchange(),
			Type:        DexType,
			Timestamp:   time.Now().Unix(),
			Reserves:    entity.PoolReserves{"0", "0"},
			Tokens:      tokens,
			Extra:       "{}",
			StaticExtra: string(staticExtraBytes),
		}
		pools = append(pools, pool)
	}

	// Update subgraph-mode metadata
	if len(subgraphPools) > 0 {
		lastCreatedAtTimestamp, err := strconv.Atoi(subgraphPools[len(subgraphPools)-1].CreatedAtTimestamp)
		if err != nil {
			return nil, err
		}
		metadata.LastCreatedAtTimestamp = lastCreatedAtTimestamp
		metadata.LastProcessedPoolId = subgraphPools[len(subgraphPools)-1].ID
	}

	return pools, nil
}

func (u *PoolsListUpdater) getPoolsList(ctx context.Context, lastCreatedAtTimestamp int, first int) ([]SubgraphPool, error) {
	req := graphqlpkg.NewRequest(getPoolsListQuery(lastCreatedAtTimestamp, first))

	var response struct {
		Pools []SubgraphPool `json:"pools"`
	}

	if err := u.graphqlClient.Run(ctx, req, &response); err != nil {
		logger.WithFields(logger.Fields{
			"dexId": u.config.DexID,
			"error": err,
		}).Errorf("failed to query subgraph")
		return nil, err
	}

	return response.Pools, nil
}

// deduplicatePools merges pool slices, keeping the first occurrence of each pool address.
// Subgraph pools take precedence over RPC pools when both sources return the same pool.
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
