package uniswapv4

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type Config struct {
	ChainID                int    `json:"chainID"`
	DexID                  string `json:"dexID"`
	SubgraphAPI            string `json:"subgraphAPI"`
	UniversalRouterAddress string `json:"universalRouterAddress"`
	Permit2Address         string `json:"permit2Address"`
	Multicall3Address      string `json:"multicall3Address"`
	StateViewAddress       string `json:"stateViewAddress"`
	NewPoolLimit           int    `json:"newPoolLimit"`
	AllowSubgraphError     bool   `json:"allowSubgraphError"`

	TimeThresholdByPool map[string]time.Duration `json:"timeThreshold"` // blocks swap after any event

	FetchTickFromStateView bool // instead of fetching from subgraph

	HookConfigs map[common.Address]any `json:"hookConfigs" mapstructure:"hookConfigs"`

	// Pool discovery modes (both can be enabled simultaneously; results are deduplicated)
	EnableSubgraphUpdater bool   `json:"enableSubgraphUpdater"` // discover pools via subgraph (default path)
	EnableRPCUpdater      bool   `json:"enableRPCUpdater"`      // discover pools via RPC using list_of_pools.json
	PoolManagerAddress    string `json:"poolManagerAddress"`    // PoolManager contract address for eth_getLogs filter
	PoolsFile             string `json:"poolsFile"`             // path to pools JSON file; empty = use embedded list_of_pools.json
}

func (c *Config) IsAllowSubgraphError() bool {
	return c.AllowSubgraphError
}
