package klik

import (
	"fmt"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

const (
	weth        = "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2"
	launchToken = "0x6965db0623c03982bf357a82d25f66b361f0d993"
)

// buildPool mirrors feehookv3's test pool builder: a synthetic full-range
// ETH/token v4 pool, tracked with a fixed FeeBps so the test is independent
// of live chain/market-cap state. See that package's buildPool for why
// full-range liquidity is enough to prove the hook's delta math.
func buildPool(t *testing.T, hooks string, feeBps int) entity.Pool {
	t.Helper()

	hookExtra := ""
	if feeBps >= 0 {
		hookExtra = fmt.Sprintf(`,\"hX\":{\"f\":%d,\"c0\":true}`, feeBps)
	}

	poolJSON := fmt.Sprintf(`{
		"address":"0xfeedbeef00000000000000000000000000000000000000000000000000002",
		"swapFee":0,
		"exchange":"uniswap-v4",
		"type":"uniswap-v4",
		"timestamp":1700000000,
		"reserves":["5000000000000000000000000000","5000000000000000000000000000"],
		"tokens":[
			{"address":"%s","symbol":"WETH","decimals":18,"swappable":true},
			{"address":"%s","symbol":"TOKEN","decimals":18,"swappable":true}
		],
		"extra":"{\"liquidity\":5000000000000000000000000,\"sqrtPriceX96\":79228162514264337593543950336,\"tickSpacing\":200,\"tick\":0,\"ticks\":[{\"index\":-887200,\"liquidityGross\":5000000000000000000000000,\"liquidityNet\":5000000000000000000000000},{\"index\":887200,\"liquidityGross\":5000000000000000000000000,\"liquidityNet\":-5000000000000000000000000}]%s}",
		"staticExtra":"{\"0x0\":[true,false],\"fee\":0,\"tS\":200,\"hooks\":\"%s\"}",
		"blockNumber":1
	}`, weth, launchToken, hookExtra, hooks)

	var p entity.Pool
	require.NoError(t, json.Unmarshal([]byte(poolJSON), &p))
	return p
}

// TestCalcAmountOut_FeeMatchesVanillaPoolExactly is the Klik counterpart of
// feehookv3's test of the same name: same delta-hook shape, same
// specified-vs-unspecified-side split, just a different (lower) fee bps.
// See that package's test for the full reasoning.
func TestCalcAmountOut_FeeMatchesVanillaPoolExactly(t *testing.T) {
	t.Parallel()

	const feeBps = 125 // 1.25%, the live-verified first tier for this hook's canary pool
	zeroAddr := "0x0000000000000000000000000000000000000000"

	noHookPool := buildPool(t, zeroAddr, -1)
	hookPool := buildPool(t, HookAddresses[0].Hex(), feeBps)

	noHookSim, err := uniswapv4.NewPoolSimulator(noHookPool, valueobject.ChainIDEthereum)
	require.NoError(t, err)
	hookSim, err := uniswapv4.NewPoolSimulator(hookPool, valueobject.ChainIDEthereum)
	require.NoError(t, err)

	amountIn := "1000000000000000000" // 1 ETH

	t.Run("buy: fee taken pre-swap from the ETH input", func(t *testing.T) {
		t.Parallel()
		fee := mulDivDown(amountIn, feeBps, 10_000)
		netIn := subStr(amountIn, fee)

		expected, err := noHookSim.CalcAmountOut(pool.CalcAmountOutParams{
			TokenAmountIn: pool.TokenAmount{Token: weth, Amount: bigFromStr(netIn)},
			TokenOut:      launchToken,
		})
		require.NoError(t, err)

		got, err := hookSim.CalcAmountOut(pool.CalcAmountOutParams{
			TokenAmountIn: pool.TokenAmount{Token: weth, Amount: bigFromStr(amountIn)},
			TokenOut:      launchToken,
		})
		require.NoError(t, err)

		require.Equal(t, expected.TokenAmountOut.Amount.String(), got.TokenAmountOut.Amount.String())
	})

	t.Run("sell: fee taken post-swap from the realized ETH output", func(t *testing.T) {
		t.Parallel()
		tokenAmountIn := "1000000000000000000000" // 1000 tokens

		rawOut, err := noHookSim.CalcAmountOut(pool.CalcAmountOutParams{
			TokenAmountIn: pool.TokenAmount{Token: launchToken, Amount: bigFromStr(tokenAmountIn)},
			TokenOut:      weth,
		})
		require.NoError(t, err)

		fee := mulDivDown(rawOut.TokenAmountOut.Amount.String(), feeBps, 10_000)
		expectedOut := subStr(rawOut.TokenAmountOut.Amount.String(), fee)

		got, err := hookSim.CalcAmountOut(pool.CalcAmountOutParams{
			TokenAmountIn: pool.TokenAmount{Token: launchToken, Amount: bigFromStr(tokenAmountIn)},
			TokenOut:      weth,
		})
		require.NoError(t, err)

		require.Equal(t, expectedOut, got.TokenAmountOut.Amount.String())
	})
}
