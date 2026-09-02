// Package ethfee is a generic base for Uniswap v4 hooks that charge a
// percentage swap fee denominated in ONE fixed pool currency -- almost
// always the native/wrapped-native side of a launch-token pool -- via
// beforeSwapReturnDelta + afterSwapReturnDelta, without touching the
// underlying AMM curve at all.
//
// This is the shape shared by nearly every memecoin-launchpad fee hook on
// v4 (FeeHook/FeeHookV2/FeeHookV3, UniversalKlikHook, LaunchHook, TaxHook,
// TTTHook, ...): the swap math is always "take X bps of the ETH side, let
// the rest go through the pool untouched." Only the fee SCHEDULE -- how the
// current bps is derived: flat, tiered by market cap, decaying, ... --
// differs between them, and that lives in each concrete hook's Track().
//
// To add a new hook in this family: embed ethfee.Hook, set FeeBps and
// FeeCurrencyIsToken0 from Track(), and nothing else. See the feehookv3 and
// klik packages for worked examples.
package ethfee

import (
	"context"
	"math/big"

	"github.com/KyberNetwork/ethrpc"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/bignumber"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// FeeBpsDenom is the basis-point denominator (10_000 = 100%) used by this
// whole family of launchpad fee hooks -- FeeHook/FeeHookV2/FeeHookV3's
// SPLIT_DENOM and UniversalKlikHook's BASIS_POINTS_DIVISOR are both 10_000.
// A hook whose contract uses a different denominator (e.g. ppm) should
// rescale to this one in its own Track() rather than change this constant.
var FeeBpsDenom = big.NewInt(10_000)

// Hook implements the delta-hook wiring shared by every hook in this
// family. It is meant to be embedded by concrete packages that supply
// Track() (how FeeBps gets refreshed) and RegisterHooksFactory (which
// on-chain addresses use it).
//
// FeeBps is the CURRENT total fee, out of FeeBpsDenom, valid for the pool
// state as of the last Track(). FeeCurrencyIsToken0 records which pool
// token the fee is denominated in (almost always the native/wrapped-native
// token, which -- because v4 always sorts the native currency first --
// means this is almost always true).
//
// Unsupported/UnsupportedErr are optional: a concrete hook whose on-chain
// _beforeSwap unconditionally reverts for pools with no native/wrapped-
// native side (FeeHookV3, mcapfee, lpfeepips, feebps all do) sets these from
// New() instead of hand-rolling its own BeforeSwap/AfterSwap/CloneState
// overrides -- both fields live directly on this type, so the CloneState
// below already preserves them correctly with no per-package override
// needed. A hook that has no such gate (e.g. klik, which charges zero fee
// instead of reverting) simply leaves them unset.
type Hook struct {
	*uniswapv4.BaseHook `json:"-"`

	FeeBps              *big.Int `json:"f"`
	FeeCurrencyIsToken0 bool     `json:"c0"`

	Unsupported    bool  `json:"-"`
	UnsupportedErr error `json:"-"`
}

// feeCurrencySpecified reports whether the fee currency is the side of the
// swap the caller specified an exact amount for, as opposed to the side the
// AMM curve computes. Every hook in this family charges its fee on
// whichever side that is: pre-swap (BeforeSwap.DeltaSpecified) when it's
// known exactly up front, post-swap (AfterSwap.HookFee) once the curve has
// run and the other side is known.
//
// Derivation: this simulator only ever asks a v4 pool one of two questions
// -- CalcAmountOut (exact-input: given tokenIn, find tokenOut) or
// CalcAmountIn (exact-output: given tokenOut, find tokenIn). On-chain, a v4
// hook sees `exactInput := amountSpecified < 0`, and every hook in this
// family gates its fee on `zeroForOne == exactInput`. Since CalcAmountOut
// always corresponds to exactInput=true on-chain and CalcAmountIn always
// corresponds to exactInput=false, that on-chain condition reduces to
// `ZeroForOne == CalcOut` when the fee currency is token0, and to its
// negation when the fee currency is token1 -- i.e. the XNOR of all three
// booleans below.
func (h *Hook) feeCurrencySpecified(calcOut, zeroForOne bool) bool {
	return calcOut == (zeroForOne == h.FeeCurrencyIsToken0)
}

// BeforeSwap charges the fee when the fee currency is the specified side:
// the exact pre-fee amount is already known (params.AmountSpecified is
// exactly what the on-chain hook reads as its ethAmount/specifiedAmount),
// so the fee can be deducted before the AMM curve runs.
func (h *Hook) BeforeSwap(params *uniswapv4.BeforeSwapParams) (*uniswapv4.BeforeSwapResult, error) {
	if h.Unsupported {
		return nil, h.UnsupportedErr
	}
	deltaSpecified := bignumber.ZeroBI
	if h.FeeBps != nil && h.FeeBps.Sign() > 0 && h.feeCurrencySpecified(params.CalcOut, params.ZeroForOne) {
		deltaSpecified = bignumber.MulDivDown(new(big.Int), params.AmountSpecified, h.FeeBps, FeeBpsDenom)
	}
	return &uniswapv4.BeforeSwapResult{
		DeltaSpecified:   deltaSpecified,
		DeltaUnspecified: bignumber.ZeroBI,
	}, nil
}

// AfterSwap charges the fee when the fee currency is the UNspecified side:
// its amount is only known once the AMM curve has actually run, so the fee
// is taken from the realized amount (AmountOut for CalcOut, AmountIn for
// CalcIn) after the fact -- mirroring the on-chain hook reading the actual
// BalanceDelta in _afterSwap instead of params.amountSpecified.
func (h *Hook) AfterSwap(params *uniswapv4.AfterSwapParams) (*uniswapv4.AfterSwapResult, error) {
	if h.Unsupported {
		return nil, h.UnsupportedErr
	}
	hookFee := bignumber.ZeroBI
	if h.FeeBps != nil && h.FeeBps.Sign() > 0 && !h.feeCurrencySpecified(params.CalcOut, params.ZeroForOne) {
		amount := lo.Ternary(params.CalcOut, params.AmountOut, params.AmountIn)
		hookFee = bignumber.MulDivDown(new(big.Int), amount, h.FeeBps, FeeBpsDenom)
	}
	return &uniswapv4.AfterSwapResult{HookFee: hookFee}, nil
}

// CloneState returns a shallow copy of the *ethfee.Hook data (FeeBps is
// never mutated in place -- Track() always installs a fresh pointer -- so a
// shallow copy is safe).
//
// NOTE for anyone adding fields to a concrete hook that embeds this type:
// because BeforeSwap/AfterSwap/CloneState are defined directly on
// ethfee.Hook (not on uniswapv4.Hook), Go's method promotion means this
// CloneState always returns dynamic type *ethfee.Hook, never the embedding
// package's own type. That's harmless as long as the embedding hook adds no
// extra fields of its own (FeeBps/FeeCurrencyIsToken0/Exchange/Unsupported/
// UnsupportedErr all survive correctly, and Track() is never called on a
// cloned pool). If a future hook in this family DOES need extra mutable
// state beyond what's declared on this type, override CloneState in that
// package the way hooks/st0x does.
func (h *Hook) CloneState() uniswapv4.Hook {
	cloned := *h
	return &cloned
}

// NativeCurrencyIsToken0 reports which pool token is the native/wrapped-
// native side, for hooks whose fee is denominated in native ETH (the common
// case). ok is false if neither token is native, which for an ETH-fee hook
// means the pool can never legitimately charge (or, for hooks like
// FeeHookV3 that unconditionally require currency0 to be native, that every
// swap on the pool reverts on-chain).
func NativeCurrencyIsToken0(pool *entity.Pool, chainID valueobject.ChainID) (isToken0, ok bool) {
	if valueobject.IsWrappedNative(pool.Tokens[0].Address, chainID) {
		return true, true
	}
	if valueobject.IsWrappedNative(pool.Tokens[1].Address, chainID) {
		return false, true
	}
	return false, false
}

// TrackCurrentFeeBps reads the current fee, in bps, for hooks that expose it
// directly via a `currentFeeBps(PoolKey) view` getter (FeeHookV3 and mcapfee
// both do -- see each package's constant.go for how their addresses were
// vetted). The fee schedule itself (tier table, custom fees, decay curve)
// lives entirely on-chain; this just builds the PoolKey from the pool's
// StaticExtra and makes the one call, so callers only need Track() to be
// `return ethfee.TrackCurrentFeeBps(ctx, param, hookABI)`.
func TrackCurrentFeeBps(ctx context.Context, param *uniswapv4.HookParam, hookABI abi.ABI) (*big.Int, error) {
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

	return feeBps, nil
}
