package dexT1

import (
	"context"

	"github.com/KyberNetwork/ethrpc"
	"github.com/ethereum/go-ethereum/common"
)

// GetPoolTokens fetches the Token0 and Token1 addresses for a single Fluid DexT1
// pool by calling getPoolReservesAdjusted on the DexReservesResolver. Returns
// zero addresses on error (the error is also returned).
func GetPoolTokens(ctx context.Context, httpClient *ethrpc.Client, resolverAddr string, poolAddr string) (token0, token1 common.Address, err error) {
	result := &PoolWithReserves{}
	req := httpClient.NewRequest().SetContext(ctx)
	req.AddCall(&ethrpc.Call{
		ABI:    dexReservesResolverABI,
		Target: resolverAddr,
		Method: DRRMethodGetPoolReservesAdjusted,
		Params: []any{common.HexToAddress(poolAddr)},
	}, []any{result})
	if _, err := req.TryAggregate(); err != nil {
		return common.Address{}, common.Address{}, err
	}
	return result.Token0Address, result.Token1Address, nil
}
