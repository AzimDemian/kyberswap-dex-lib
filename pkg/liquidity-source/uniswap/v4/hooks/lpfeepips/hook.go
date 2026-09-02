package lpfeepips

import (
	"context"
	"math/big"

	"github.com/KyberNetwork/ethrpc"
	"github.com/goccy/go-json"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/bignumber"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// Hook prices an unverified family of hooks (see HookAddresses) that charge
// a flat LP_FEE_PIPS()/BASIS_POINTS() fraction via beforeSwapReturnDelta/
// afterSwapReturnDelta, structurally identical to the FeeHookV3/klik shape
// that ethfee.Hook already generalizes -- the only per-deployment detail is
// which view functions to read the current fee from.
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
		BaseHook:       &uniswapv4.BaseHook{Exchange: valueobject.ExchangeUniswapV4LpFeePips},
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

// Track reads LP_FEE_PIPS() and BASIS_POINTS() and rescales to ethfee's
// FeeBpsDenom (10_000) rather than assuming they already match it -- both
// observed deployments currently report BASIS_POINTS()=10000 (i.e. no
// rescale needed today), but BASIS_POINTS is a per-deployment value on this
// hook, not a fixed protocol constant, so a future instance could use a
// different denominator.
func (h *Hook) Track(ctx context.Context, param *uniswapv4.HookParam) (json.RawMessage, error) {
	var lpFeePips, basisPoints *big.Int
	if _, err := param.RpcClient.NewRequest().SetContext(ctx).SetBlockNumber(param.BlockNumber).
		AddCall(&ethrpc.Call{
			ABI:    hookABI,
			Target: param.HookAddress.Hex(),
			Method: "LP_FEE_PIPS",
		}, []any{&lpFeePips}).
		AddCall(&ethrpc.Call{
			ABI:    hookABI,
			Target: param.HookAddress.Hex(),
			Method: "BASIS_POINTS",
		}, []any{&basisPoints}).
		Aggregate(); err != nil {
		return nil, err
	}

	if basisPoints == nil || basisPoints.Sign() == 0 {
		h.FeeBps = big.NewInt(0)
	} else {
		h.FeeBps = bignumber.MulDivDown(new(big.Int), lpFeePips, ethfee.FeeBpsDenom, basisPoints)
	}

	return json.Marshal(h)
}

var _ uniswapv4.Hook = (*Hook)(nil)
