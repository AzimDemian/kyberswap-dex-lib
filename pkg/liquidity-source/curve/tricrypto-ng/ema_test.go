package tricryptong

import (
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
)

// poolJSON is a realistic pool entity taken from the existing test fixtures.
const emaPoolJSON = `{
  "address":"0x2889302a794da87fbf1d6db415c1492194663d13",
  "exchange":"curve-tricrypto-ng",
  "type":"curve-tricrypto-ng",
  "timestamp":1710842900,
  "reserves":["3848079508071253519125552","60997386412794855327","1028200997183081004168"],
  "tokens":[
    {"address":"0xf939e0a03fb07f59a73314e73794be0e57ac1b4e","symbol":"crvUSD","decimals":18,"swappable":true},
    {"address":"0x18084fba666a33d37592fa2633fd49a74dd93a88","symbol":"tBTC","decimals":18,"swappable":true},
    {"address":"0x7f39c581f595b53c5cb19bd0b3f8da6c935e2ca0","symbol":"wstETH","decimals":18,"swappable":true}
  ],
  "extra":"{\"InitialA\":\"1707629\",\"InitialGamma\":\"11809167828997\",\"InitialAGammaTime\":1705051559,\"FutureA\":\"540000\",\"FutureGamma\":\"80500000000000\",\"FutureAGammaTime\":1705537322,\"D\":\"11990883592127090140834712\",\"PriceScale\":[\"66313464177401058702341\",\"3988288337309167729564\"],\"PriceOracle\":[\"63612706012126486095056\",\"3782761569503404058823\"],\"LastPrices\":[\"63608488224235038716789\",\"3782322291001686876800\"],\"LastPricesTimestamp\":1710838775,\"FeeGamma\":\"400000000000000\",\"MidFee\":\"1000000\",\"OutFee\":\"140000000\",\"LpSupply\":\"6209561906175920711602\",\"XcpProfit\":\"1005532234158713186\",\"VirtualPrice\":\"1002781276086899355\",\"AllowedExtraProfit\":\"100000000\",\"AdjustmentStep\":\"100000000000\",\"MaTime\":\"866\"}",
  "staticExtra":"{\"IsNativeCoins\":[false,false,false]}",
  "blockNumber":19468099
}`

// helper: build a PoolSimulator from the fixture JSON.
func newEMATestPool(t *testing.T) *PoolSimulator {
	t.Helper()
	var poolEntity entity.Pool
	err := json.Unmarshal([]byte(emaPoolJSON), &poolEntity)
	require.NoError(t, err)
	p, err := NewPoolSimulator(poolEntity)
	require.NoError(t, err)
	return p
}

func TestEMA_NoDecay_WhenTimestampIsCurrent(t *testing.T) {
	p := newEMATestPool(t)

	// Set LastPricesTimestamp to "now" so there is zero elapsed time.
	p.Extra.LastPricesTimestamp = time.Now().Unix()

	result := p.currentPriceOracle()

	// With dt == 0 the function should return the stored PriceOracle unchanged.
	require.Len(t, result, len(p.Extra.PriceOracle))
	for i := range result {
		assert.Equal(t, p.Extra.PriceOracle[i].Dec(), result[i].Dec(),
			"index %d: expected PriceOracle when dt==0", i)
	}
}

func TestEMA_DecayedTowardLastPrices(t *testing.T) {
	p := newEMATestPool(t)

	// Use a typical MaTime: 866 seconds (~= 600 * ln(2)).
	p.Extra.MaTime = uint256.NewInt(866)

	// Set LastPricesTimestamp to 60 seconds in the past.
	p.Extra.LastPricesTimestamp = time.Now().Unix() - 60

	result := p.currentPriceOracle()

	require.Len(t, result, len(p.Extra.PriceOracle))
	for i := range result {
		oracle := &p.Extra.PriceOracle[i]
		lastP := &p.Extra.LastPrices[i]
		r := &result[i]

		// Determine which end is lower / higher so the "between" check works
		// regardless of whether LastPrices > PriceOracle or vice-versa.
		lo, hi := new(uint256.Int).Set(lastP), new(uint256.Int).Set(oracle)
		if lo.Cmp(hi) > 0 {
			lo, hi = hi, lo
		}

		assert.True(t, r.Cmp(lo) >= 0, "index %d: result %s < lower bound %s",
			i, r.Dec(), lo.Dec())
		assert.True(t, r.Cmp(hi) <= 0, "index %d: result %s > upper bound %s",
			i, r.Dec(), hi.Dec())

		// The result must NOT equal the stored oracle (some decay must happen).
		assert.NotEqual(t, oracle.Dec(), r.Dec(),
			"index %d: expected some EMA decay but result equals stored oracle", i)
	}
}

func TestEMA_FallbackWhenMaTimeNil(t *testing.T) {
	p := newEMATestPool(t)

	// Nil MaTime should cause the function to return the stored PriceOracle.
	p.Extra.MaTime = nil
	p.Extra.LastPricesTimestamp = time.Now().Unix() - 60

	result := p.currentPriceOracle()

	require.Len(t, result, len(p.Extra.PriceOracle))
	for i := range result {
		assert.Equal(t, p.Extra.PriceOracle[i].Dec(), result[i].Dec(),
			"index %d: expected PriceOracle fallback when MaTime is nil", i)
	}
}
