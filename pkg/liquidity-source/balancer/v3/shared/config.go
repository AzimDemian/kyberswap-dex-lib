package shared

import (
	"net/http"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

type Config struct {
	DexID            string              `json:"dexID,omitempty"`
	ChainID          valueobject.ChainID `json:"chainID,omitempty"`
	PoolType         string              `json:"poolType,omitempty"`
	SubgraphAPI      string              `json:"subgraphAPI,omitempty"`
	SubgraphHeaders  http.Header         `json:"subgraphHeaders,omitempty"`
	NewPoolLimit     int                 `json:"newPoolLimit,omitempty"`
	VaultExplorer    string              `json:"vaultExplorer"`
	SubgraphChain    string              `json:"subgraphChain"`
	SubgraphPoolType string              `json:"-"`
	// FactoryAddress enables RPC-based pool discovery (no subgraph required).
	// When set, GetNewPools uses factory enumeration instead of the subgraph.
	FactoryAddress string `json:"factoryAddress,omitempty"`
	// HookTypes maps hook contract addresses (lower-case) to their HookType.
	// Required for RPC-based discovery so the correct hook type can be stored
	// in StaticExtra without hitting an additional RPC round-trip per pool.
	HookTypes map[string]HookType `json:"hookTypes,omitempty"`
}
