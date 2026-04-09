package dexT1

import (
	"context"
	"strings"

	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/common"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// getPoolsFromStaticList fetches pool metadata for a batch of pre-configured pool addresses
// using the DexReservesResolver's single-pool method. Uses TryAggregate so individual
// pool failures do not abort the batch; failed calls leave the PoolWithReserves zero-valued,
// detected by a zero Token0Address. The cursor always advances past the batch.
func (u *PoolsListUpdater) getPoolsFromStaticList(ctx context.Context, fromIndex, batchSize int) ([]entity.Pool, int, error) {
	if fromIndex >= len(u.config.StaticPoolList) {
		return nil, fromIndex, nil
	}

	end := min(fromIndex+batchSize, len(u.config.StaticPoolList))
	batch := u.config.StaticPoolList[fromIndex:end]

	// Wrap in a single-field struct so go-ethereum's ABI copyAtomic sees Field(0) as
	// PoolWithReserves (kind=Struct) and routes through setStruct. Without the wrapper,
	// Field(0) would be PoolAddress (common.Address = [20]byte, kind=Array) and setArray
	// would panic trying to call Len() on the decoded struct value (go-ethereum v1.15.2+).
	type poolResultWrapper struct{ PoolWithReserves }
	poolResults := make([]poolResultWrapper, len(batch))

	req := u.ethrpcClient.R().SetContext(ctx)
	for i, addr := range batch {
		req.AddCall(&ethrpc.Call{
			ABI:    dexReservesResolverABI,
			Target: u.config.DexReservesResolver,
			Method: DRRMethodGetPoolReservesAdjusted,
			Params: []any{common.HexToAddress(addr)},
		}, []any{&poolResults[i]})
	}

	if _, err := req.TryAggregate(); err != nil {
		return nil, fromIndex, err
	}

	zeroAddr := common.Address{}
	tokenSet := make(map[common.Address]struct{})
	var valid []int

	for i := range poolResults {
		if poolResults[i].Token0Address == zeroAddr || poolResults[i].Token1Address == zeroAddr {
			logger.WithFields(logger.Fields{
				"dexType": DexType,
				"address": batch[i],
			}).Warn("getPoolsFromStaticList: failed to fetch pool or pool has zero token address, skipping")
			continue
		}
		valid = append(valid, i)
		tokenSet[poolResults[i].Token0Address] = struct{}{}
		tokenSet[poolResults[i].Token1Address] = struct{}{}
	}

	decimalsMap := u.fetchStaticTokenDecimals(ctx, tokenSet)

	pools := make([]entity.Pool, 0, len(valid))
	for _, i := range valid {
		cur := poolResults[i].PoolWithReserves
		dec0 := decimalsMap[cur.Token0Address]
		dec1 := decimalsMap[cur.Token1Address]
		pool, err := buildPool(cur, dec0, dec1, u.config.ChainID, u.config.DexReservesResolver)
		if err != nil {
			logger.WithFields(logger.Fields{
				"dexType": DexType,
				"address": batch[i],
				"error":   err,
			}).Warn("getPoolsFromStaticList: failed to build pool, skipping")
			continue
		}
		pools = append(pools, pool)
	}

	return pools, end, nil
}

// fetchStaticTokenDecimals fetches ERC20 decimals for a set of token addresses in a single
// multicall. Native token addresses are assigned 18 decimals without an RPC call.
// Failures for individual tokens result in a zero decimal value in the destination — callers
// should treat 0 as 18 if a fallback is needed (the decimals map is not authoritative on failure).
func (u *PoolsListUpdater) fetchStaticTokenDecimals(ctx context.Context, tokens map[common.Address]struct{}) map[common.Address]uint8 {
	result := make(map[common.Address]uint8, len(tokens))

	type entry struct {
		addr     common.Address
		decimals uint8
	}
	var rpcTokens []entry

	for addr := range tokens {
		if strings.EqualFold(addr.String(), valueobject.NativeAddress) {
			result[addr] = 18
			continue
		}
		rpcTokens = append(rpcTokens, entry{addr: addr})
	}

	if len(rpcTokens) == 0 {
		return result
	}

	decimalsOut := make([]uint8, len(rpcTokens))
	req := u.ethrpcClient.R().SetContext(ctx)
	for i, e := range rpcTokens {
		req.AddCall(&ethrpc.Call{
			ABI:    erc20,
			Target: e.addr.String(),
			Method: TokenMethodDecimals,
		}, []any{&decimalsOut[i]})
	}

	if _, err := req.TryAggregate(); err != nil {
		logger.WithFields(logger.Fields{
			"dexType": DexType,
			"error":   err,
		}).Warn("fetchStaticTokenDecimals: multicall failed, defaulting all to 18")
		for _, e := range rpcTokens {
			result[e.addr] = 18
		}
		return result
	}

	for i, e := range rpcTokens {
		dec := decimalsOut[i]
		if dec == 0 {
			dec = 18 // fallback for failed individual calls
		}
		result[e.addr] = dec
	}

	return result
}
