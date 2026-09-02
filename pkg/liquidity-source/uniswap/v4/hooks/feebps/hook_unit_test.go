package feebps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
)

func TestUnsupportedPoolRejectsSwaps(t *testing.T) {
	t.Parallel()

	h := &Hook{Hook: ethfee.Hook{
		BaseHook: &uniswapv4.BaseHook{}, Unsupported: true, UnsupportedErr: ErrPoolHasNoNativeCurrency,
	}}

	_, err := h.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency)

	_, err = h.AfterSwap(&uniswapv4.AfterSwapParams{BeforeSwapParams: &uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: false}})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency)
}

// TestCloneState_PreservesUnsupportedGuard is the same regression class
// documented on feehookv3's identical test -- here it also guards that the
// clone keeps the right `m` method-name pair, since (unlike
// Unsupported/UnsupportedErr, which now live on ethfee.Hook itself) `m` is
// feebps-specific and only survives because feebps.Hook still overrides
// CloneState.
func TestCloneState_PreservesUnsupportedGuard(t *testing.T) {
	t.Parallel()

	h := &Hook{Hook: ethfee.Hook{
		BaseHook: &uniswapv4.BaseHook{}, Unsupported: true, UnsupportedErr: ErrPoolHasNoNativeCurrency,
	}, m: bpsDenomMethods}
	cloned := h.CloneState()

	typed, isFeeBps := cloned.(*Hook)
	require.True(t, isFeeBps, "clone must keep dynamic type *feebps.Hook, got %T", cloned)
	assert.Equal(t, bpsDenomMethods, typed.m)

	_, err := cloned.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency, "clone must keep rejecting swaps on an unsupported pool")
}
