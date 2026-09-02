package mcapfee

import (
	"testing"

	"github.com/stretchr/testify/assert"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
)

// TestUnsupportedPoolRejectsSwaps mirrors the assumed behavior for a pool
// with no native/wrapped-native side -- see type.go.
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

// TestCloneState_PreservesUnsupportedGuard is a regression test for the same
// bug class documented on feehookv3's identical test: ethfee.Hook's
// CloneState wins by method promotion unless overridden, which would
// silently drop an extra field a concrete hook added itself. mcapfee.Hook
// adds none -- Unsupported/UnsupportedErr live directly on ethfee.Hook --
// so it relies on the embedded CloneState as-is.
func TestCloneState_PreservesUnsupportedGuard(t *testing.T) {
	t.Parallel()

	h := &Hook{Hook: ethfee.Hook{
		BaseHook: &uniswapv4.BaseHook{}, Unsupported: true, UnsupportedErr: ErrPoolHasNoNativeCurrency,
	}}
	cloned := h.CloneState()

	_, err := cloned.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency, "clone must keep rejecting swaps on an unsupported pool")
}
