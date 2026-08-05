package twocryptong

import (
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
)

func makeEMATestSimulator(t *testing.T, lastPricesTs int64, maTime *uint256.Int) *PoolSimulator {
	t.Helper()

	// Use first fixture from pool_simulator_test.go (ETH+/WETH on Arbitrum)
	var pool entity.Pool
	require.NoError(t, json.Unmarshal([]byte(pools[0]), &pool))

	sim, err := NewPoolSimulator(pool)
	require.NoError(t, err)

	sim.Extra.LastPricesTimestamp = lastPricesTs
	sim.Extra.MaTime = maTime

	return sim
}

func TestTwoCryptoEMA_NoDecay_WhenTimestampIsCurrent(t *testing.T) {
	now := time.Now().Unix()
	sim := makeEMATestSimulator(t, now, uint256.NewInt(866))

	result := sim.currentPriceOracle()

	assert.Equal(t, len(sim.Extra.PriceOracle), len(result))
	for i := range result {
		assert.Equal(t, sim.Extra.PriceOracle[i].String(), result[i].String(),
			"oracle[%d] should be unchanged when timestamp is current", i)
	}
}

func TestTwoCryptoEMA_DecayedTowardLastPrices(t *testing.T) {
	now := time.Now().Unix()
	sim := makeEMATestSimulator(t, now-60, uint256.NewInt(866))

	result := sim.currentPriceOracle()

	for i := range result {
		oracle := &sim.Extra.PriceOracle[i]
		lastPrice := &sim.Extra.LastPrices[i]

		lo := new(uint256.Int)
		hi := new(uint256.Int)
		if oracle.Cmp(lastPrice) < 0 {
			lo.Set(oracle)
			hi.Set(lastPrice)
		} else {
			lo.Set(lastPrice)
			hi.Set(oracle)
		}

		assert.True(t, result[i].Cmp(lo) >= 0 && result[i].Cmp(hi) <= 0,
			"oracle[%d] should be between LastPrices and PriceOracle: got %s, range [%s, %s]",
			i, result[i].String(), lo.String(), hi.String())

		assert.NotEqual(t, oracle.String(), result[i].String(),
			"oracle[%d] should have decayed (not equal to snapshot)", i)
	}
}

func TestTwoCryptoEMA_FallbackWhenMaTimeNil(t *testing.T) {
	now := time.Now().Unix()
	sim := makeEMATestSimulator(t, now-60, nil)

	result := sim.currentPriceOracle()

	for i := range result {
		assert.Equal(t, sim.Extra.PriceOracle[i].String(), result[i].String(),
			"oracle[%d] should fallback to snapshot when MaTime is nil", i)
	}
}
