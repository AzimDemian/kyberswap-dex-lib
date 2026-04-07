package uniswapv3

import "net/http"

type Config struct {
	DexID              string
	SubgraphAPI        string      `json:"subgraphAPI,omitempty"`
	SubgraphHeaders    http.Header `json:"subgraphHeaders,omitempty"`
	AllowSubgraphError bool        `json:"allowSubgraphError,omitempty"`
	AllowSubgraphFetch bool        `json:"allowSubgraphFetch,omitempty"`
	TickLensAddress    string      `json:"tickLensAddress,omitempty"`
	AlwaysUseTickLens  bool        `json:"alwaysUseTickLens,omitempty"`
	AllowRPCFetch      bool        `json:"allowRPCFetch,omitempty"`
	StaticPoolList     []string    `json:"staticPoolList,omitempty"`
	RPCBatchSize       int         `json:"rpcBatchSize,omitempty"`
}

func (c *Config) IsAllowSubgraphError() bool {
	return c.AllowSubgraphError
}
