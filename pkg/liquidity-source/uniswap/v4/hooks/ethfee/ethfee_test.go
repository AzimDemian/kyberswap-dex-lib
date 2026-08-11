package ethfee

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
)

// feeCurrencySpecified must reproduce, for every (CalcOut, ZeroForOne)
// combination this simulator ever calls a hook with, the exact
// `zeroForOne == exactInput` condition every hook in this family gates its
// fee on -- derived by hand from FeeHookV3._beforeSwap/_afterSwap and
// UniversalKlikHook._isBuyTransaction+isExactInput, using the fact that
// CalcAmountOut always corresponds to on-chain exactInput=true and
// CalcAmountIn always corresponds to on-chain exactInput=false.
func TestFeeCurrencySpecified(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                string
		calcOut, zeroForOne bool
		feeCurrencyIsToken0 bool
		want                bool
	}{
		// fee currency = token0 (the common case: native ETH always sorts
		// first, so this is what FeeHookV3 and Klik use in practice)
		{"buy, exact-in, fee=token0", true, true, true, true},     // exact-input ETH->token: ETH is specified
		{"sell, exact-in, fee=token0", true, false, true, false},  // exact-input token->ETH: ETH is the AMM output
		{"buy, exact-out, fee=token0", false, true, true, false},  // exact-output token->ETH-in: ETH is the AMM input
		{"sell, exact-out, fee=token0", false, false, true, true}, // exact-output ETH-out: ETH is specified

		// fee currency = token1 (mirror image, e.g. a hook that supports
		// native on either side)
		{"buy, exact-in, fee=token1", true, true, false, false},
		{"sell, exact-in, fee=token1", true, false, false, true},
		{"buy, exact-out, fee=token1", false, true, false, true},
		{"sell, exact-out, fee=token1", false, false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &Hook{FeeCurrencyIsToken0: tc.feeCurrencyIsToken0}
			assert.Equal(t, tc.want, h.feeCurrencySpecified(tc.calcOut, tc.zeroForOne))
		})
	}
}

func TestBeforeSwap_FeesSpecifiedSideOnly(t *testing.T) {
	t.Parallel()

	h := &Hook{FeeBps: big.NewInt(150), FeeCurrencyIsToken0: true} // 1.5%, fee in token0

	// CalcOut + ZeroForOne=true: token0 (fee currency) is specified -> charged.
	res, err := h.BeforeSwap(&uniswapv4.BeforeSwapParams{
		CalcOut: true, ZeroForOne: true, AmountSpecified: big.NewInt(1_000_000),
	})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(15_000), res.DeltaSpecified) // floor(1_000_000 * 150 / 10_000)
	assert.Equal(t, big.NewInt(0), res.DeltaUnspecified)

	// Rounds down, matching Solidity's truncating integer division.
	res, err = h.BeforeSwap(&uniswapv4.BeforeSwapParams{
		CalcOut: true, ZeroForOne: true, AmountSpecified: big.NewInt(333),
	})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(4), res.DeltaSpecified) // floor(333 * 150 / 10_000) = floor(4.995) = 4

	// CalcOut + ZeroForOne=false: token1 is specified, fee currency
	// (token0) is the AMM's unspecified output -> not charged here.
	res, err = h.BeforeSwap(&uniswapv4.BeforeSwapParams{
		CalcOut: true, ZeroForOne: false, AmountSpecified: big.NewInt(1_000_000),
	})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), res.DeltaSpecified)
}

func TestAfterSwap_FeesUnspecifiedSideOnly(t *testing.T) {
	t.Parallel()

	h := &Hook{FeeBps: big.NewInt(150), FeeCurrencyIsToken0: true}

	// CalcOut + ZeroForOne=false: fee currency (token0) is the realized AMM
	// output -> charged against AmountOut.
	res, err := h.AfterSwap(&uniswapv4.AfterSwapParams{
		BeforeSwapParams: &uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: false},
		AmountIn:         big.NewInt(1_000_000),
		AmountOut:        big.NewInt(2_000_000),
	})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(30_000), res.HookFee) // floor(2_000_000 * 150 / 10_000)

	// CalcOut + ZeroForOne=true: fee currency was already charged in
	// BeforeSwap -> AfterSwap must not double-charge it.
	res, err = h.AfterSwap(&uniswapv4.AfterSwapParams{
		BeforeSwapParams: &uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true},
		AmountIn:         big.NewInt(1_000_000),
		AmountOut:        big.NewInt(2_000_000),
	})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), res.HookFee)
}

func TestZeroFeeIsANoOp(t *testing.T) {
	t.Parallel()

	h := &Hook{FeeBps: big.NewInt(0), FeeCurrencyIsToken0: true}
	res, err := h.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true, AmountSpecified: big.NewInt(1_000_000)})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), res.DeltaSpecified)

	afterRes, err := h.AfterSwap(&uniswapv4.AfterSwapParams{
		BeforeSwapParams: &uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: false},
		AmountOut:        big.NewInt(1_000_000),
	})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), afterRes.HookFee)
}

func TestCloneState_PreservesFeeState(t *testing.T) {
	t.Parallel()

	h := &Hook{
		BaseHook:            &uniswapv4.BaseHook{Exchange: "uniswap-v4-test"},
		FeeBps:              big.NewInt(150),
		FeeCurrencyIsToken0: true,
	}
	cloned := h.CloneState()

	assert.Equal(t, "uniswap-v4-test", cloned.GetExchange())
	res, err := cloned.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true, AmountSpecified: big.NewInt(1_000_000)})
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(15_000), res.DeltaSpecified, "clone must keep pricing with the same FeeBps as the original")
}
