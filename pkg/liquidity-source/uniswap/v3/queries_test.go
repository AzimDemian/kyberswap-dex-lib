package uniswapv3

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQueriesUniswapV3_GetDiscoveryPoolsListQuery(t *testing.T) {
	t.Parallel()

	t.Run("it should include subgraphError and TVL/volume filters when set", func(t *testing.T) {
		actual := getDiscoveryPoolsListQuery(true, 1000, 0, 10000.0, 5000.0)

		assert.Contains(t, actual, "subgraphError: allow")
		assert.Contains(t, actual, "totalValueLockedUSD_gt:")
		assert.Contains(t, actual, "volumeUSD_gt:")
		assert.Contains(t, actual, "orderBy: totalValueLockedUSD")
		assert.Contains(t, actual, "orderDirection: desc")
		assert.Contains(t, actual, `liquidity_not: "0"`)
	})

	t.Run("it should omit subgraphError and filters when not set", func(t *testing.T) {
		actual := getDiscoveryPoolsListQuery(false, 1000, 0, 0, 0)

		assert.False(t, strings.Contains(actual, "subgraphError: allow"))
		assert.False(t, strings.Contains(actual, "totalValueLockedUSD_gt:"))
		assert.False(t, strings.Contains(actual, "volumeUSD_gt:"))
		assert.Contains(t, actual, "orderBy: totalValueLockedUSD")
		assert.Contains(t, actual, "orderDirection: desc")
		assert.Contains(t, actual, `liquidity_not: "0"`)
	})
}

func TestQueriesUniswapV3_GetPoolTicksQuery(t *testing.T) {
	t.Parallel()

	t.Run("it should return correct query when allowing subgraph error", func(t *testing.T) {
		expect := fmt.Sprintf(`{
		ticks(
			subgraphError: allow,
			where: {
				pool: "%v"
				tickIdx_gt: %v,
				liquidityGross_not: 0
			},
			orderBy: tickIdx,
			orderDirection: asc,
			first: 1000
		) {
			tickIdx
			liquidityNet
			liquidityGross
		}
	}`, "abc", "0")

		actual := getPoolTicksQuery(true, "abc", "0")

		assert.Equal(t, expect, actual)
	})

	t.Run("it should return correct query when subgraph error is not allowed", func(t *testing.T) {
		expect := fmt.Sprintf(`{
		ticks(
			
			where: {
				pool: "%v"
				tickIdx_gt: %v,
				liquidityGross_not: 0
			},
			orderBy: tickIdx,
			orderDirection: asc,
			first: 1000
		) {
			tickIdx
			liquidityNet
			liquidityGross
		}
	}`, "abc", "0")

		actual := getPoolTicksQuery(false, "abc", "0")

		assert.Equal(t, expect, actual)
	})
}
