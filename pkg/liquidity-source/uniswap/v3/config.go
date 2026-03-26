package uniswapv3

type Config struct {
	DexID           string
	TickLensAddress string   `json:"tickLensAddress,omitempty"`
	AllowRPCFetch   bool     `json:"allowRPCFetch,omitempty"`
	StaticPoolList  []string `json:"staticPoolList,omitempty"`
	RPCBatchSize    int      `json:"rpcBatchSize,omitempty"`
}
