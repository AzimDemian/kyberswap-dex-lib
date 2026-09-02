package feehookv3

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
	launchToken = "0xf209b3d7cf01b726f01f5980d614a764dbf435c9"
)

// buildPool returns a synthetic, full-range (single tick range spanning the
// whole usable price space) ETH/token v4 pool at tickSpacing 200, tracked
// with a fixed FeeBps so the test doesn't depend on live chain state. Using
// full-range liquidity keeps the underlying v3 curve close to a constant-
// product AMM, which is all that matters here: the point of this test is
// to prove the HOOK's delta math composes correctly with whatever curve
// runs underneath, not to re-verify the v3 tick-crossing engine (that's
// covered by the uniswapv3 package's own tests).
func buildPool(t *testing.T, hooks string, feeBps int) entity.Pool {
	t.Helper()

	hookExtra := ""
	if feeBps >= 0 {
		hookExtra = fmt.Sprintf(`,\"hX\":{\"f\":%d,\"c0\":true}`, feeBps)
	}

	poolJSON := fmt.Sprintf(`{
		"address":"0xfeedbeef00000000000000000000000000000000000000000000000000001",
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

// TestCalcAmountOut_FeeMatchesVanillaPoolExactly proves the hook's delta
// math composes correctly with the real v3 curve underneath, to the wei, by
// comparing against a hookless pool with identical liquidity/price/ticks:
//   - buying (ETH in, exact input): the fee is taken off the ETH input
//     BEFORE the curve runs, so out_withHook(amountIn) must equal
//     out_noHook(amountIn - fee) exactly -- not out_noHook(amountIn)*(1-fee),
//     since the v3 curve isn't linear.
//   - selling (token in, exact input): the fee is taken from the realized
//     ETH output AFTER the curve runs, so
//     out_withHook = out_noHook(amountIn) - floor(out_noHook(amountIn)*bps/10000).
func TestCalcAmountOut_FeeMatchesVanillaPoolExactly(t *testing.T) {
	t.Parallel()

	const feeBps = 150 // 1.5%, the live-verified rate for this hook's canary pool
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

func TestCalcAmountIn_FeeMatchesVanillaPoolExactly(t *testing.T) {
	t.Parallel()

	const feeBps = 150
	zeroAddr := "0x0000000000000000000000000000000000000000"

	noHookPool := buildPool(t, zeroAddr, -1)
	hookPool := buildPool(t, HookAddresses[0].Hex(), feeBps)

	noHookSim, err := uniswapv4.NewPoolSimulator(noHookPool, valueobject.ChainIDEthereum)
	require.NoError(t, err)
	hookSim, err := uniswapv4.NewPoolSimulator(hookPool, valueobject.ChainIDEthereum)
	require.NoError(t, err)

	t.Run("buy exact tokens out: fee added on top of the required ETH input", func(t *testing.T) {
		t.Parallel()
		tokensOut := "1000000000000000000000" // 1000 tokens, exact output

		expected, err := noHookSim.CalcAmountIn(pool.CalcAmountInParams{
			TokenAmountOut: pool.TokenAmount{Token: launchToken, Amount: bigFromStr(tokensOut)},
			TokenIn:        weth,
		})
		require.NoError(t, err)

		got, err := hookSim.CalcAmountIn(pool.CalcAmountInParams{
			TokenAmountOut: pool.TokenAmount{Token: launchToken, Amount: bigFromStr(tokensOut)},
			TokenIn:        weth,
		})
		require.NoError(t, err)

		// fee is charged on the realized ETH input, i.e. on
		// expected.TokenAmountIn.Amount -- so ceil-free floor division must
		// exactly reconcile in - hookIn.
		rawIn := expected.TokenAmountIn.Amount.String()
		fee := mulDivDown(rawIn, feeBps, 10_000)
		expectedIn := addStr(rawIn, fee)
		require.Equal(t, expectedIn, got.TokenAmountIn.Amount.String())
	})

	t.Run("sell exact ETH out: fee added to the target ETH before the curve runs", func(t *testing.T) {
		t.Parallel()
		ethOut := "1000000000000000000" // exactly 1 ETH out, net of fee

		fee := mulDivDown(ethOut, feeBps, 10_000)
		grossEthOut := addStr(ethOut, fee)

		expected, err := noHookSim.CalcAmountIn(pool.CalcAmountInParams{
			TokenAmountOut: pool.TokenAmount{Token: weth, Amount: bigFromStr(grossEthOut)},
			TokenIn:        launchToken,
		})
		require.NoError(t, err)

		got, err := hookSim.CalcAmountIn(pool.CalcAmountInParams{
			TokenAmountOut: pool.TokenAmount{Token: weth, Amount: bigFromStr(ethOut)},
			TokenIn:        launchToken,
		})
		require.NoError(t, err)

		require.Equal(t, expected.TokenAmountIn.Amount.String(), got.TokenAmountIn.Amount.String())
	})
}
