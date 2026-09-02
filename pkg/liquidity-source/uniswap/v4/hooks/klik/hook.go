package klik

import (
	"context"
	"math/big"
	"sync"

	"github.com/KyberNetwork/ethrpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"

	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// Hook prices Klik's UniversalKlikHook (v2) pools: a market-cap-tiered ETH
// tax on both buys and sells, taken via beforeSwapReturnDelta /
// afterSwapReturnDelta exactly like feehookv3.Hook. The fee schedule here
// is simpler than FeeHookV3's (no per-token custom fee, no partner/module
// splits), but it takes two dependent on-chain reads instead of one:
// getFeeTiers() only lives on the hook, and the market cap only lives on
// whatever factory the hook currently points at.
//
// Not modeled: UniversalKlikHook charges zero fee on the exact block a pool
// is initialized (poolDeploymentBlock). That's a single-block window with
// no practical routing impact, so Track() doesn't special-case it.
type Hook struct {
	ethfee.Hook

	// nativePaired mirrors _getTokenFromPool returning address(0): if
	// neither pool token is native, the hook charges no fee on either side
	// (unlike FeeHookV3, this is NOT a revert -- the pool just trades fee-
	// free), so Track() skips the RPC calls entirely.
	nativePaired bool
}

var _ = uniswapv4.RegisterHooksFactory(func(param *uniswapv4.HookParam) uniswapv4.Hook {
	return New(param)
}, HookAddresses...)

// New builds a Klik hook from pool/HookExtra state. Exported so tests can
// construct one directly without going through the address registry.
func New(param *uniswapv4.HookParam) *Hook {
	hook := &Hook{Hook: ethfee.Hook{
		BaseHook: &uniswapv4.BaseHook{Exchange: valueobject.ExchangeUniswapV4Klik},
	}}
	_ = param.HookExtra.Unmarshal(hook)

	if pool := param.Pool; pool != nil && len(pool.Tokens) == 2 {
		isToken0, ok := ethfee.NativeCurrencyIsToken0(pool, param.Cfg.ChainID)
		hook.FeeCurrencyIsToken0 = isToken0
		hook.nativePaired = ok
	}
	return hook
}

func (h *Hook) Track(ctx context.Context, param *uniswapv4.HookParam) (json.RawMessage, error) {
	if !h.nativePaired {
		h.FeeBps = big.NewInt(0)
		return json.Marshal(h)
	}

	launchToken := param.Pool.Tokens[0].Address
	if h.FeeCurrencyIsToken0 {
		launchToken = param.Pool.Tokens[1].Address
	}

	factoryAddr, err := factoryOf(ctx, param)
	if err != nil {
		return nil, err
	}

	if factoryAddr == (common.Address{}) {
		h.FeeBps = fixedFeeFallbackBps
		return json.Marshal(h)
	}

	var (
		tiers []feeTier
		mcap  *big.Int
	)
	req := param.RpcClient.NewRequest().SetContext(ctx).SetBlockNumber(param.BlockNumber)
	req.AddCall(&ethrpc.Call{
		ABI:    hookABI,
		Target: param.HookAddress.Hex(),
		Method: "getFeeTiers",
	}, []any{&tiers}).AddCall(&ethrpc.Call{
		ABI:    factoryABI,
		Target: factoryAddr.Hex(),
		Method: "getMarketCap",
		Params: []any{common.HexToAddress(launchToken)},
	}, []any{&mcap})
	if _, err := req.Aggregate(); err != nil {
		return nil, err
	}

	h.FeeBps = feeBpsForMarketCap(tiers, mcap)

	return json.Marshal(h)
}

// factoryCache holds each Klik hook address's factory() result, keyed by
// hook address. factory() is a hook-level constant -- every pool that shares
// a hook address shares the same factory -- so re-reading it via RPC on
// every single pool's every single Track() call is pure waste; this caches
// it for the life of the process instead.
var factoryCache sync.Map // common.Address -> common.Address

// factoryOf returns param.HookAddress's factory() address, reading it via
// RPC only on the first call for a given hook address.
func factoryOf(ctx context.Context, param *uniswapv4.HookParam) (common.Address, error) {
	if cached, ok := factoryCache.Load(param.HookAddress); ok {
		return cached.(common.Address), nil
	}

	var factoryAddr common.Address
	if _, err := param.RpcClient.NewRequest().SetContext(ctx).SetBlockNumber(param.BlockNumber).AddCall(&ethrpc.Call{
		ABI:    hookABI,
		Target: param.HookAddress.Hex(),
		Method: "factory",
	}, []any{&factoryAddr}).Call(); err != nil {
		return common.Address{}, err
	}

	factoryCache.Store(param.HookAddress, factoryAddr)
	return factoryAddr, nil
}

// CloneState is overridden (rather than inherited from ethfee.Hook) because
// `nativePaired` lives on *this* type, not on ethfee.Hook -- see ethfee.Hook's
// doc comment for why the embedded CloneState alone wouldn't preserve it.
// nativePaired is currently only read by Track() (never called on a cloned
// pool), so this is a latent-correctness guard for future BeforeSwap/
// AfterSwap overrides more than a currently-observable bug.
func (h *Hook) CloneState() uniswapv4.Hook {
	cloned := *h
	return &cloned
}

var _ uniswapv4.Hook = (*Hook)(nil)
