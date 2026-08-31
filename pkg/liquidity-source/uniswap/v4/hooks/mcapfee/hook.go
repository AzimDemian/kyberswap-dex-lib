package mcapfee

import (
	"context"

	"github.com/goccy/go-json"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// Hook prices the market-cap-tiered launchpad fee hooks listed in
// HookAddresses: a beforeSwapReturnDelta/afterSwapReturnDelta ETH fee, same
// shape as FeeHookV3/klik/lpfeepips (see ../ethfee), whose current bps is
// read directly from the hook's own currentFeeBps(PoolKey) view rather than
// reimplemented here -- see constant.go for how these addresses were vetted.
// ethfee.Hook.Unsupported carries the no-native-currency revert gate, so no
// BeforeSwap/AfterSwap/CloneState override is needed here.
type Hook struct {
	ethfee.Hook
}

var _ = uniswapv4.RegisterHooksFactory(func(param *uniswapv4.HookParam) uniswapv4.Hook {
	return New(param)
}, HookAddresses...)

// New builds a Hook from pool/HookExtra state. Exported so tests can
// construct one directly without going through the address registry.
func New(param *uniswapv4.HookParam) *Hook {
	hook := &Hook{Hook: ethfee.Hook{
		BaseHook:       &uniswapv4.BaseHook{Exchange: valueobject.ExchangeUniswapV4McapFee},
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
