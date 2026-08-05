package pancakev3

import "net/http"

type Config struct {
	DexID              string
	SubgraphAPI        string      `json:"subgraphAPI"`
	SubgraphHeaders    http.Header `json:"subgraphHeaders"`
	AllowSubgraphError bool        `json:"allowSubgraphError"`
	AllowSubgraphFetch bool        `json:"allowSubgraphFetch"`

	AlwaysUseTickLens bool // instead of fetching from subgraph
	TickLensAddress   string

	// AllowRPCFetch enables pool discovery via direct RPC calls against the
	// addresses in StaticPoolList. Lets a chain run without a subgraph.
	AllowRPCFetch  bool     `json:"allowRPCFetch"`
	StaticPoolList []string `json:"staticPoolList"`
	RPCBatchSize   int      `json:"rpcBatchSize"`
}

func (c *Config) IsAllowSubgraphError() bool {
	return c.AllowSubgraphError
}
