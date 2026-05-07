package shared

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/kutils/klog"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	graphqlpkg "github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/graphql"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

type (
	PoolsListUpdater struct {
		config        *Config
		ethrpcClient  *ethrpc.Client
		graphqlClient *graphqlpkg.Client
		count         int
	}

	// Metadata covers both subgraph and RPC discovery modes.
	// LastCreateTime and Skip are used in subgraph mode.
	// FactoryOffset is used in RPC mode (factory.getPools start index).
	Metadata struct {
		LastCreateTime int64 `json:"lastCreateTime"`
		Skip           int   `json:"skip"`
		FactoryOffset  int   `json:"factoryOffset"`
	}
)

func NewPoolsListUpdater(config *Config, ethrpcClient *ethrpc.Client,
	graphqlClient *graphqlpkg.Client) *PoolsListUpdater {
	return &PoolsListUpdater{
		config:        config,
		ethrpcClient:  ethrpcClient,
		graphqlClient: graphqlClient,
	}
}

func (u *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	if u.config.FactoryAddress == "" {
		if !u.config.AllowSubgraphFetch {
			return nil, metadataBytes, nil
		}
		return u.getNewPoolsSubgraph(ctx, metadataBytes)
	}
	pools, meta, err := u.getNewPoolsRPC(ctx, metadataBytes)
	if err == nil {
		return pools, meta, nil
	}
	if u.config.AllowSubgraphFetch {
		klog.WithFields(ctx, klog.Fields{"dexID": u.config.DexID}).
			Warnf("RPC pool discovery failed, falling back to subgraph: %v", err)
		return u.getNewPoolsSubgraph(ctx, metadataBytes)
	}
	return nil, nil, err
}

// ── Subgraph mode ─────────────────────────────────────────────────────────────

func (u *PoolsListUpdater) getNewPoolsSubgraph(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	l := klog.WithFields(ctx, klog.Fields{"dexID": u.config.DexID})
	l.Infof("Start updating pools list (subgraph)...")
	var pools []entity.Pool
	defer func() {
		l.WithFields(klog.Fields{"count": len(pools)}).Infof("Finish updating pools list.")
	}()

	var metadata Metadata
	if len(metadataBytes) > 0 && u.count%RelistInterval > 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, nil, err
		}
	}
	u.count++

	subgraphPools, metadata, err := u.querySubgraph(ctx, metadata)
	if err != nil {
		return nil, nil, err
	} else if len(subgraphPools) == 0 {
		return nil, metadataBytes, nil
	}

	newMetadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, nil, err
	}

	pools, err = u.initPools(ctx, subgraphPools)
	return pools, newMetadataBytes, err
}

func (u *PoolsListUpdater) querySubgraph(ctx context.Context, metadata Metadata) ([]*SubgraphPool, Metadata, error) {
	var response struct {
		Pools []*SubgraphPool `json:"poolGetPools"`
	}

	req := graphqlpkg.NewRequest(SubgraphPoolsQuery)
	req.Var(VarChain, u.config.SubgraphChain)
	req.Var(VarPoolType, u.config.SubgraphPoolType)
	req.Var(VarCreateTimeGt, metadata.LastCreateTime)
	req.Var(VarFirst, u.config.NewPoolLimit)
	req.Var(VarSkip, metadata.Skip)

	if err := u.graphqlClient.Run(ctx, req, &response); err != nil {
		return nil, metadata, err
	}

	if poolCnt := len(response.Pools); poolCnt >= u.config.NewPoolLimit {
		metadata.Skip += u.config.NewPoolLimit
	} else if poolCnt > 0 {
		metadata.LastCreateTime = response.Pools[len(response.Pools)-1].CreateTime
		metadata.Skip = 0
	}

	return response.Pools, metadata, nil
}

func (u *PoolsListUpdater) initPools(ctx context.Context, subgraphPools []*SubgraphPool) ([]entity.Pool, error) {
	bufferSet := mapset.NewThreadUnsafeSet[string]()
	for _, subgraphPool := range subgraphPools {
		for _, token := range subgraphPool.PoolTokens {
			if isBuffer := token.CanUseBufferForSwaps &&
				!lo.ContainsBy(subgraphPool.PoolTokens, func(t SubgraphToken) bool {
					// don't use as buffer token if the underlying token is already contained in the pool as a main token
					return token.UnderlyingToken.Address == t.Address
				}); isBuffer {
				bufferSet.Add(token.Address)
			}
		}
	}
	buffers := bufferSet.ToSlice()
	bufferAssets := make([]common.Address, len(buffers))
	req := u.ethrpcClient.R().SetContext(ctx)
	for i, buffer := range buffers {
		req.AddCall(&ethrpc.Call{
			ABI:    VaultExplorerABI,
			Target: u.config.VaultExplorer,
			Method: VaultMethodGetBufferAsset,
			Params: []any{common.HexToAddress(buffer)},
		}, []any{&bufferAssets[i]})
	}
	if _, err := req.TryAggregate(); err != nil {
		return nil, err
	}
	for i, bufferAsset := range bufferAssets {
		if bufferAsset == valueobject.AddrZero {
			bufferSet.Remove(buffers[i])
		}
	}

	pools := make([]entity.Pool, len(subgraphPools))
	for i, subgraphPool := range subgraphPools {
		bufferTokens := make([]string, len(subgraphPool.PoolTokens))
		poolTokens := make([]*entity.PoolToken, len(subgraphPool.PoolTokens))
		reserves := make([]string, len(subgraphPool.PoolTokens))
		registeredTokens := make([]string, len(subgraphPool.PoolTokens))
		var bufferPairs []BufferPair

		for j, token := range subgraphPool.PoolTokens {
			isBuffer := token.CanUseBufferForSwaps && bufferSet.ContainsOne(token.Address) &&
				!lo.ContainsBy(subgraphPool.PoolTokens, func(t SubgraphToken) bool {
					// don't use as buffer token if the underlying token is already contained in the pool as a main token
					return token.UnderlyingToken.Address == t.Address
				})
			registeredTokens[j] = strings.ToLower(token.Address)
			if isBuffer {
				underlyingStr := strings.ToLower(token.UnderlyingToken.Address)
				bufferTokens[j] = strings.ToLower(token.Address)
				poolTokens[j] = &entity.PoolToken{Address: underlyingStr, Swappable: true}
				bufferPairs = append(bufferPairs, BufferPair{
					Underlying: underlyingStr,
					Wrapped:    strings.ToLower(token.Address),
				})
			} else {
				bufferTokens[j] = ""
				poolTokens[j] = &entity.PoolToken{Address: strings.ToLower(token.Address), Swappable: true}
			}
			reserves[j] = "0"
		}
		for _, bufferToken := range bufferTokens {
			if bufferToken != "" {
				poolTokens = append(poolTokens, &entity.PoolToken{
					Address:   bufferToken,
					Swappable: true,
				})
				reserves = append(reserves, "0")
			}
		}

		staticExtraBytes, err := json.Marshal(&StaticExtra{
			Hook:             subgraphPool.Hook.Address,
			HookType:         subgraphPool.Hook.Type,
			BufferTokens:     bufferTokens,
			RegisteredTokens: registeredTokens,
			Buffers:          bufferPairs,
		})
		if err != nil {
			return nil, err
		}

		pools[i] = entity.Pool{
			Address:     subgraphPool.Address,
			Exchange:    u.config.DexID,
			Type:        u.config.PoolType,
			Timestamp:   time.Now().Unix(),
			Tokens:      poolTokens,
			Reserves:    reserves,
			StaticExtra: string(staticExtraBytes),
		}
	}

	return pools, nil
}

// ── RPC mode ──────────────────────────────────────────────────────────────────

func (u *PoolsListUpdater) getNewPoolsRPC(ctx context.Context, metadataBytes []byte) ([]entity.Pool, []byte, error) {
	l := klog.WithFields(ctx, klog.Fields{"dexID": u.config.DexID})
	l.Infof("Start updating pools list (RPC)...")
	var pools []entity.Pool
	defer func() {
		l.WithFields(klog.Fields{"count": len(pools)}).Infof("Finish updating pools list.")
	}()

	var metadata Metadata
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return nil, nil, err
		}
	}

	// Step 1: get total pool count from factory.
	var poolCount *big.Int
	req := u.ethrpcClient.R().SetContext(ctx)
	req.AddCall(&ethrpc.Call{
		ABI:    PoolFactoryABI,
		Target: u.config.FactoryAddress,
		Method: FactoryMethodGetPoolCount,
	}, []any{&poolCount})
	if _, err := req.TryAggregate(); err != nil {
		return nil, nil, err
	}
	if poolCount == nil {
		return nil, metadataBytes, nil
	}

	currentCount := int(poolCount.Int64())
	lastOffset := metadata.FactoryOffset
	if currentCount <= lastOffset {
		return nil, metadataBytes, nil
	}

	// Respect NewPoolLimit per run.
	fetchCount := currentCount - lastOffset
	if u.config.NewPoolLimit > 0 && fetchCount > u.config.NewPoolLimit {
		fetchCount = u.config.NewPoolLimit
	}

	// Step 2: fetch the new pool addresses.
	var newPools []common.Address
	req2 := u.ethrpcClient.R().SetContext(ctx)
	req2.AddCall(&ethrpc.Call{
		ABI:    PoolFactoryABI,
		Target: u.config.FactoryAddress,
		Method: FactoryMethodGetPools,
		Params: []any{big.NewInt(int64(lastOffset)), big.NewInt(int64(fetchCount))},
	}, []any{&newPools})
	if _, err := req2.TryAggregate(); err != nil {
		return nil, nil, err
	}
	if len(newPools) == 0 {
		return nil, metadataBytes, nil
	}

	// Step 3: batch-fetch token lists and hook configs for all new pools.
	tokenLists := make([][]common.Address, len(newPools))
	hooksConfigs := make([]HooksConfigRPC, len(newPools))

	req3 := u.ethrpcClient.R().SetContext(ctx)
	for i, poolAddr := range newPools {
		p := poolAddr // capture
		req3.AddCall(&ethrpc.Call{
			ABI:    VaultExplorerABI,
			Target: u.config.VaultExplorer,
			Method: VaultMethodGetPoolTokens,
			Params: []any{p},
		}, []any{&tokenLists[i]})
		req3.AddCall(&ethrpc.Call{
			ABI:    VaultExplorerABI,
			Target: u.config.VaultExplorer,
			Method: VaultMethodGetHooksConfig,
			Params: []any{p},
		}, []any{&hooksConfigs[i]})
	}
	if _, err := req3.TryAggregate(); err != nil {
		return nil, nil, err
	}

	// Step 4: for each token across all pools, call getBufferAsset.
	// Collect unique token addresses first.
	allTokenSet := mapset.NewThreadUnsafeSet[string]()
	for _, tokens := range tokenLists {
		for _, t := range tokens {
			allTokenSet.Add(strings.ToLower(t.Hex()))
		}
	}
	allTokens := allTokenSet.ToSlice()
	bufferAssets := make(map[string]common.Address, len(allTokens))
	bufferAssetResults := make([]common.Address, len(allTokens))

	req4 := u.ethrpcClient.R().SetContext(ctx)
	for i, tok := range allTokens {
		req4.AddCall(&ethrpc.Call{
			ABI:    VaultExplorerABI,
			Target: u.config.VaultExplorer,
			Method: VaultMethodGetBufferAsset,
			Params: []any{common.HexToAddress(tok)},
		}, []any{&bufferAssetResults[i]})
	}
	if _, err := req4.TryAggregate(); err != nil {
		return nil, nil, err
	}
	for i, tok := range allTokens {
		if bufferAssetResults[i] != valueobject.AddrZero {
			bufferAssets[tok] = bufferAssetResults[i]
		}
	}

	// Step 5: build entity.Pool for each new pool.
	pools = make([]entity.Pool, 0, len(newPools))
	for i, poolAddr := range newPools {
		tokens := tokenLists[i]
		if len(tokens) == 0 {
			continue
		}

		hookAddr := hooksConfigs[i].HooksConfigData.HooksContract
		hookType := u.hookTypeForAddr(hookAddr)

		bufferTokens := make([]string, len(tokens))
		poolTokens := make([]*entity.PoolToken, len(tokens))
		reserves := make([]string, len(tokens))
		registeredTokens := make([]string, len(tokens))
		var bufferPairs []BufferPair

		for j, tok := range tokens {
			key := strings.ToLower(tok.Hex())
			registeredTokens[j] = key
			if underlying, ok := bufferAssets[key]; ok {
				underlyingStr := strings.ToLower(underlying.Hex())
				bufferTokens[j] = key
				poolTokens[j] = &entity.PoolToken{Address: underlyingStr, Swappable: true}
				bufferPairs = append(bufferPairs, BufferPair{Underlying: underlyingStr, Wrapped: key})
			} else {
				bufferTokens[j] = ""
				poolTokens[j] = &entity.PoolToken{Address: key, Swappable: true}
			}
			reserves[j] = "0"
		}
		for _, bufTok := range bufferTokens {
			if bufTok != "" {
				poolTokens = append(poolTokens, &entity.PoolToken{Address: strings.ToLower(bufTok), Swappable: true})
				reserves = append(reserves, "0")
			}
		}

		staticExtraBytes, err := json.Marshal(&StaticExtra{
			Hook:             strings.ToLower(hookAddr.Hex()),
			HookType:         hookType,
			BufferTokens:     bufferTokens,
			RegisteredTokens: registeredTokens,
			Buffers:          bufferPairs,
		})
		if err != nil {
			return nil, nil, err
		}

		pools = append(pools, entity.Pool{
			Address:     strings.ToLower(poolAddr.Hex()),
			Exchange:    u.config.DexID,
			Type:        u.config.PoolType,
			Timestamp:   time.Now().Unix(),
			Tokens:      poolTokens,
			Reserves:    reserves,
			StaticExtra: string(staticExtraBytes),
		})
	}

	metadata.FactoryOffset = lastOffset + fetchCount
	newMetadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, nil, err
	}

	return pools, newMetadataBytes, nil
}

// hookTypeForAddr looks up the HookType for a hook contract address using the
// registry configured in Config.HookTypes. Returns "" if addr is the zero
// address or the address is not in the registry.
func (u *PoolsListUpdater) hookTypeForAddr(addr common.Address) HookType {
	if addr == valueobject.AddrZero {
		return ""
	}
	if u.config.HookTypes == nil {
		return ""
	}
	key := strings.ToLower(addr.Hex())
	if ht, ok := u.config.HookTypes[key]; ok {
		return ht
	}
	klog.Warnf(context.Background(), "balancer v3 RPC discovery: unknown hook address %s for dex %s — pool will be treated as having no hook", addr.Hex(), u.config.DexID)
	return ""
}
