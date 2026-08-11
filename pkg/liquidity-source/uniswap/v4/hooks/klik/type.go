package klik

import "math/big"

// feeTier mirrors UniversalKlikHook's on-chain FeeTier struct (tuple
// decoding target for getFeeTiers()). Tiers are sorted ascending by
// MCapThresholdETH; the last tier's threshold is ignored (it's the floor
// that catches every market cap above the second-to-last tier).
type feeTier struct {
	MCapThresholdETH *big.Int
	TotalBps         *big.Int
	PlatformBps      *big.Int
}

// fixedFeeFallbackBps mirrors FIXED_FEE_FALLBACK: the flat fee charged
// when the hook has no factory configured (so market cap can't be looked
// up) or, defensively, no fee tiers at all.
var fixedFeeFallbackBps = big.NewInt(100) // 1%

// feeBpsForMarketCap replicates UniversalKlikHook.getFeeTier(mcapETH)'s
// scan exactly: walk the ascending tiers, return the first whose threshold
// the market cap is still under, falling through to the last (floor) tier.
func feeBpsForMarketCap(tiers []feeTier, mcapETH *big.Int) *big.Int {
	if len(tiers) == 0 {
		return fixedFeeFallbackBps
	}
	for i, tier := range tiers {
		if i == len(tiers)-1 || mcapETH.Cmp(tier.MCapThresholdETH) < 0 {
			return tier.TotalBps
		}
	}
	return fixedFeeFallbackBps // unreachable, mirrors the on-chain fallback
}
