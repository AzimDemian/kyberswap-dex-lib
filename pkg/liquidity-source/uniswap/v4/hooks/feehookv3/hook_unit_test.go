package feehookv3

import (
	"testing"

	"github.com/stretchr/testify/assert"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
)

// TestUnsupportedPoolRejectsSwaps mirrors FeeHookV3's unconditional
// `revert PoolMustHaveEthAsCurrency0()` check: a pool carrying this hook
// without a native/wrapped-native side must reject every swap, not price
// it as fee-free.
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

// TestCloneState_PreservesUnsupportedGuard is a regression test for a bug
// class that's easy to hit in this codebase: ethfee.Hook.BeforeSwap/
// AfterSwap/CloneState are defined directly on *ethfee.Hook, so a concrete
// hook that adds its own extra fields WITHOUT overriding CloneState gets a
// clone whose dynamic type is *ethfee.Hook, silently dropping those fields.
// feehookv3.Hook has no such extra fields -- Unsupported/UnsupportedErr live
// directly on ethfee.Hook -- so it relies on the embedded CloneState as-is;
// this test would fail if that guard stopped surviving a clone.
func TestCloneState_PreservesUnsupportedGuard(t *testing.T) {
	t.Parallel()

	h := &Hook{Hook: ethfee.Hook{
		BaseHook: &uniswapv4.BaseHook{}, Unsupported: true, UnsupportedErr: ErrPoolHasNoNativeCurrency,
	}}
	cloned := h.CloneState()

	_, err := cloned.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency, "clone must keep rejecting swaps on an unsupported pool")
}
