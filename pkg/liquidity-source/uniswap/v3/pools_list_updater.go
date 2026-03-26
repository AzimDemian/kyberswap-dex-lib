package uniswapv3

import (
	"context"
	"strings"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	poollist "github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool/list"
)

type PoolsListUpdater struct {
	config       *Config
	ethrpcClient *ethrpc.Client
}

var _ = poollist.RegisterFactoryCE(DexTypeUniswapV3, NewPoolsListUpdater)

func NewPoolsListUpdater(
	cfg *Config,
	ethrpcClient *ethrpc.Client,
) *PoolsListUpdater {
	return &PoolsListUpdater{
		config:       cfg,
		ethrpcClient: ethrpcClient,
	}
}

func (d *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	var metadata Metadata
	if len(metadataBytes) != 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, metadataBytes, err
		}
	}

	var pools []entity.Pool
	if d.config.AllowRPCFetch {
		var (
			nextIndex int
			err       error
		)
		pools, nextIndex, err = d.getPoolsFromRPC(ctx, metadata.LastProcessedRPCIndex, rpcPoolBatchSize)
		if err != nil {
			return nil, metadataBytes, err
		}
		metadata.LastProcessedRPCIndex = nextIndex
	}

	pools = deduplicatePools(pools)

	newMetadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, metadataBytes, err
	}

	logger.WithFields(logger.Fields{
		"dexId": d.config.DexID,
		"pools": len(pools),
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
