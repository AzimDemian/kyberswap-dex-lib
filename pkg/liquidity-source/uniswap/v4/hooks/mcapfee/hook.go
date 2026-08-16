package mcapfee

import (
	"context"
	"math/big"

	"github.com/KyberNetwork/ethrpc"
	"github.com/ethereum/go-ethereum/common"
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
type Hook struct {
	ethfee.Hook

	// unsupported is true when the pool has no native/wrapped-native side.
	// See ErrPoolHasNoNativeCurrency.
	unsupported bool
}

var _ = uniswapv4.RegisterHooksFactory(func(param *uniswapv4.HookParam) uniswapv4.Hook {
	return New(param)
}, HookAddresses...)

// New builds a Hook from pool/HookExtra state. Exported so tests can
// construct one directly without going through the address registry.
func New(param *uniswapv4.HookParam) *Hook {
	hook := &Hook{Hook: ethfee.Hook{
		BaseHook: &uniswapv4.BaseHook{Exchange: valueobject.ExchangeUniswapV4McapFee},
	}}
	_ = param.HookExtra.Unmarshal(hook)

	if pool := param.Pool; pool != nil && len(pool.Tokens) == 2 {
		isToken0, ok := ethfee.NativeCurrencyIsToken0(pool, param.Cfg.ChainID)
		hook.FeeCurrencyIsToken0 = isToken0
		hook.unsupported = !ok
	}
	return hook
}

func (h *Hook) Track(ctx context.Context, param *uniswapv4.HookParam) (json.RawMessage, error) {
	var staticExtra uniswapv4.StaticExtra
	if err := json.Unmarshal([]byte(param.Pool.StaticExtra), &staticExtra); err != nil {
		return nil, err
	}

	currency0, currency1 := uniswapv4.NativeTokenAddress, uniswapv4.NativeTokenAddress
	if !staticExtra.IsNative[0] {
		currency0 = common.HexToAddress(param.Pool.Tokens[0].Address)
	}
	if !staticExtra.IsNative[1] {
		currency1 = common.HexToAddress(param.Pool.Tokens[1].Address)
	}

	var feeBps *big.Int
	if _, err := param.RpcClient.NewRequest().SetContext(ctx).SetBlockNumber(param.BlockNumber).AddCall(&ethrpc.Call{
		ABI:    hookABI,
		Target: param.HookAddress.Hex(),
		Method: "currentFeeBps",
		Params: []any{uniswapv4.PoolKey{
			Currency0:   currency0,
			Currency1:   currency1,
			Fee:         big.NewInt(int64(staticExtra.Fee)),
			TickSpacing: big.NewInt(int64(staticExtra.TickSpacing)),
			Hooks:       staticExtra.HooksAddress,
		}},
	}, []any{&feeBps}).Call(); err != nil {
		return nil, err
	}

	h.FeeBps = feeBps

	return json.Marshal(h)
}

func (h *Hook) BeforeSwap(params *uniswapv4.BeforeSwapParams) (*uniswapv4.BeforeSwapResult, error) {
	if h.unsupported {
		return nil, ErrPoolHasNoNativeCurrency
	}
	return h.Hook.BeforeSwap(params)
}

func (h *Hook) AfterSwap(params *uniswapv4.AfterSwapParams) (*uniswapv4.AfterSwapResult, error) {
	if h.unsupported {
		return nil, ErrPoolHasNoNativeCurrency
	}
	return h.Hook.AfterSwap(params)
}

// CloneState is overridden rather than inherited from ethfee.Hook -- see
// feehookv3's identical override for the reasoning (unsupported lives on
// *this* type).
func (h *Hook) CloneState() uniswapv4.Hook {
	cloned := *h
	return &cloned
}

var _ uniswapv4.Hook = (*Hook)(nil)
