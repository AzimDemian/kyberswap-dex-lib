package feehookv3

import (
	"context"

	"github.com/goccy/go-json"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// Hook prices FeeHookV3 pools: a market-cap-tiered (or per-token custom,
// auto-decaying) ETH fee taken via beforeSwapReturnDelta/afterSwapReturnDelta.
// The fee schedule itself lives entirely on-chain (tier table, custom fees,
// decay curve); rather than reimplement that logic here, Track() just calls
// the hook's own `currentFeeBps(PoolKey)` view, which already does exactly
// what CalcAmountOut/CalcAmountIn need.
//
// FeeHookV3's _beforeSwap unconditionally reverts unless currency0 is native
// ETH; since v4 always sorts native first when present, that's equivalent to
// "every swap on this pool reverts on-chain" -- ethfee.Hook.Unsupported
// carries that gate, so no BeforeSwap/AfterSwap/CloneState override is
// needed here (see ethfee.Hook's doc comment).
type Hook struct {
	ethfee.Hook
}

var _ = uniswapv4.RegisterHooksFactory(func(param *uniswapv4.HookParam) uniswapv4.Hook {
	return New(param)
}, HookAddresses...)

// New builds a FeeHookV3 hook from pool/HookExtra state. Exported so tests
// (and, if useful, future hooks that only ever appear alongside FeeHookV3)
// can construct one directly without going through the address registry.
func New(param *uniswapv4.HookParam) *Hook {
	hook := &Hook{Hook: ethfee.Hook{
		BaseHook:       &uniswapv4.BaseHook{Exchange: valueobject.ExchangeUniswapV4FeeHookV3},
		UnsupportedErr: ErrPoolHasNoNativeCurrency,
	}}
	_ = param.HookExtra.Unmarshal(hook)

	if pool := param.Pool; pool != nil && len(pool.Tokens) == 2 {
		isToken0, ok := ethfee.NativeCurrencyIsToken0(pool, param.Cfg.ChainID)
		hook.FeeCurrencyIsToken0 = isToken0
		hook.Unsupported = !ok
	}
	return hook
}

func (h *Hook) Track(ctx context.Context, param *uniswapv4.HookParam) (json.RawMessage, error) {
	feeBps, err := ethfee.TrackCurrentFeeBps(ctx, param, hookABI)
	if err != nil {
		return nil, err
	}

	h.FeeBps = feeBps

	return json.Marshal(h)
}

var _ uniswapv4.Hook = (*Hook)(nil)
