package klik

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
)

func ether(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

// liveTiers is the exact default 17-tier table read live from
// UniversalKlikHook.getFeeTiers() on mainnet (0x07f17023...0a0cc) while
// building this package -- see TestTrack_LiveChain.
var liveTiers = []feeTier{
	{ether(15), big.NewInt(125), big.NewInt(80)},
	{ether(55), big.NewInt(120), big.NewInt(70)},
	{ether(90), big.NewInt(115), big.NewInt(60)},
	{ether(125), big.NewInt(110), big.NewInt(55)},
	{ether(165), big.NewInt(105), big.NewInt(52)},
	{ether(365), big.NewInt(100), big.NewInt(50)},
	{ether(545), big.NewInt(95), big.NewInt(47)},
	{ether(725), big.NewInt(90), big.NewInt(44)},
	{ether(910), big.NewInt(85), big.NewInt(41)},
	{ether(1090), big.NewInt(80), big.NewInt(38)},
	{ether(1275), big.NewInt(75), big.NewInt(35)},
	{ether(1450), big.NewInt(70), big.NewInt(33)},
	{ether(1635), big.NewInt(65), big.NewInt(30)},
	{ether(1815), big.NewInt(60), big.NewInt(28)},
	{ether(2000), big.NewInt(55), big.NewInt(27)},
	{ether(2175), big.NewInt(53), big.NewInt(25)},
	{big.NewInt(0), big.NewInt(35), big.NewInt(15)}, // floor tier, threshold ignored
}

func TestFeeBpsForMarketCap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mcap *big.Int
		want int64
	}{
		{"zero mcap -> first tier", big.NewInt(0), 125},
		{"~0.7 ETH -> first tier (live-verified)", big.NewInt(700_696_643_000_000_000), 125},
		{"just under first threshold", new(big.Int).Sub(ether(15), big.NewInt(1)), 125},
		{"exactly at first threshold -> next tier", ether(15), 120},
		{"mid-table", ether(400), 95},
		{"just under last real threshold", new(big.Int).Sub(ether(2175), big.NewInt(1)), 53},
		{"at last real threshold -> floor tier", ether(2175), 35},
		{"far above every threshold -> floor tier", ether(1_000_000), 35},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := feeBpsForMarketCap(liveTiers, tc.mcap)
			assert.Equal(t, big.NewInt(tc.want), got)
		})
	}
}

func TestFeeBpsForMarketCap_NoTiersFallsBackToFixedFee(t *testing.T) {
	t.Parallel()
	assert.Equal(t, fixedFeeFallbackBps, feeBpsForMarketCap(nil, ether(1)))
}
