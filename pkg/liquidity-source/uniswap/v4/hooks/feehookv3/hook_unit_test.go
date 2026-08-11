package feehookv3

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
)

// TestUnsupportedPoolRejectsSwaps mirrors FeeHookV3's unconditional
// `revert PoolMustHaveEthAsCurrency0()` check: a pool carrying this hook
// without a native/wrapped-native side must reject every swap, not price
// it as fee-free.
func TestUnsupportedPoolRejectsSwaps(t *testing.T) {
	t.Parallel()

	h := &Hook{Hook: ethfee.Hook{BaseHook: &uniswapv4.BaseHook{}}, unsupported: true}

	_, err := h.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency)

	_, err = h.AfterSwap(&uniswapv4.AfterSwapParams{BeforeSwapParams: &uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: false}})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency)
}

// TestCloneState_PreservesUnsupportedGuard is a regression test for a bug
// class that's easy to hit in this codebase: ethfee.Hook.BeforeSwap/
// AfterSwap/CloneState are defined directly on *ethfee.Hook, so any
// concrete hook that embeds it WITHOUT overriding CloneState gets a clone
// whose dynamic type is *ethfee.Hook -- silently dropping any extra field a
// concrete hook added (here, `unsupported`) along with whatever behavior
// depended on it. feehookv3.Hook overrides CloneState precisely to avoid
// this; this test would fail if that override were ever removed.
func TestCloneState_PreservesUnsupportedGuard(t *testing.T) {
	t.Parallel()

	h := &Hook{Hook: ethfee.Hook{BaseHook: &uniswapv4.BaseHook{}}, unsupported: true}
	cloned := h.CloneState()

	_, isFeeHookV3 := cloned.(*Hook)
	require.True(t, isFeeHookV3, "clone must keep dynamic type *feehookv3.Hook, got %T", cloned)

	_, err := cloned.BeforeSwap(&uniswapv4.BeforeSwapParams{CalcOut: true, ZeroForOne: true})
	assert.ErrorIs(t, err, ErrPoolHasNoNativeCurrency, "clone must keep rejecting swaps on an unsupported pool")
}
