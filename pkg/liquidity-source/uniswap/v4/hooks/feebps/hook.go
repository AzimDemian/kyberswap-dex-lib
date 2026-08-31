package feebps

import (
	"context"
	"math/big"

	"github.com/KyberNetwork/ethrpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/ethfee"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/util/bignumber"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// methods names the view functions a given deployment exposes for its
// current fee numerator/denominator, and optionally a graduation gate. See
// constant.go for which addresses use which set.
type methods struct {
	fee   string
	denom string

	// graduated, if non-empty, names a `graduated(address token) view
	// returns (bool)` getter that permanently zeroes the fee once a token's
	// market cap has crossed the deployment's graduation threshold -- see
	// SplitDenomAddresses's doc comment. Empty means the deployment has no
	// such gate and the fee/denom pair is always authoritative.
	graduated string
}

var (
	splitDenomMethods = methods{fee: "feeBps", denom: "SPLIT_DENOM", graduated: "graduated"}
	bpsDenomMethods   = methods{fee: "FEE_BPS", denom: "BPS_DENOM"}
)

// Hook prices a flat (non-tiered) beforeSwapReturnDelta/afterSwapReturnDelta
// ETH fee: Track() reads the numerator/denominator pair named by m and
// rescales to ethfee.FeeBpsDenom, exactly like ../lpfeepips does for its own
// LP_FEE_PIPS()/BASIS_POINTS() pair.
type Hook struct {
	ethfee.Hook
	m methods // unexported: never touched by JSON (de)serialization, set fresh per HookFactory closure
}

var _ = uniswapv4.RegisterHooksFactory(newFactory(splitDenomMethods), SplitDenomAddresses...)
var _ = uniswapv4.RegisterHooksFactory(newFactory(bpsDenomMethods), BpsDenomAddresses...)

func newFactory(m methods) uniswapv4.HookFactory {
	return func(param *uniswapv4.HookParam) uniswapv4.Hook {
		hook := &Hook{
			Hook: ethfee.Hook{
				BaseHook:       &uniswapv4.BaseHook{Exchange: valueobject.ExchangeUniswapV4FeeBps},
				UnsupportedErr: ErrPoolHasNoNativeCurrency,
			},
			m: m,
		}
		_ = param.HookExtra.Unmarshal(hook)

		if pool := param.Pool; pool != nil && len(pool.Tokens) == 2 {
			isToken0, ok := ethfee.NativeCurrencyIsToken0(pool, param.Cfg.ChainID)
			hook.FeeCurrencyIsToken0 = isToken0
			hook.Unsupported = !ok
		}
		return hook
	}
}

func (h *Hook) Track(ctx context.Context, param *uniswapv4.HookParam) (json.RawMessage, error) {
	req := param.RpcClient.NewRequest().SetContext(ctx).SetBlockNumber(param.BlockNumber)

	var fee, denom *big.Int
	req.AddCall(&ethrpc.Call{
		ABI:    hookABI,
		Target: param.HookAddress.Hex(),
		Method: h.m.fee,
	}, []any{&fee}).AddCall(&ethrpc.Call{
		ABI:    hookABI,
		Target: param.HookAddress.Hex(),
		Method: h.m.denom,
	}, []any{&denom})

	// launchToken, ok mirrors New()'s own native-side check: a pool with no
	// native/wrapped-native side is already marked h.Unsupported and rejected
	// at BeforeSwap/AfterSwap regardless of FeeBps, so if we can't identify a
	// launch token here we simply skip the graduation query rather than fail
	// Track() itself -- consistent with how `unsupported` is handled
	// everywhere else in this hook family (see feehookv3's Track(), which
	// queries currentFeeBps unconditionally too).
	var graduated bool
	if launchToken, ok := nonNativeToken(param.Pool); h.m.graduated != "" && ok {
		req.AddCall(&ethrpc.Call{
			ABI:    hookABI,
			Target: param.HookAddress.Hex(),
			Method: h.m.graduated,
			Params: []any{launchToken},
		}, []any{&graduated})
	}

	if _, err := req.Aggregate(); err != nil {
		return nil, err
	}

	switch {
	case graduated:
		// Permanently fee-free past graduation -- see SplitDenomAddresses's
		// doc comment. feeBps()/SPLIT_DENOM() still return the pre-graduation
		// rate on-chain, so this must be checked BEFORE trusting them.
		h.FeeBps = big.NewInt(0)
	case denom == nil || denom.Sign() == 0:
		h.FeeBps = big.NewInt(0)
	default:
		h.FeeBps = bignumber.MulDivDown(new(big.Int), fee, ethfee.FeeBpsDenom, denom)
	}

	return json.Marshal(h)
}

// nonNativeToken returns the pool's non-native/non-wrapped-native token
// address, for hooks whose gating functions key off the launch token rather
// than the pool itself.
func nonNativeToken(pool *entity.Pool) (common.Address, bool) {
	if pool == nil || len(pool.Tokens) != 2 {
		return common.Address{}, false
	}
	var staticExtra uniswapv4.StaticExtra
	if err := json.Unmarshal([]byte(pool.StaticExtra), &staticExtra); err != nil {
		return common.Address{}, false
	}
	switch {
	case !staticExtra.IsNative[0]:
		return common.HexToAddress(pool.Tokens[0].Address), true
	case !staticExtra.IsNative[1]:
		return common.HexToAddress(pool.Tokens[1].Address), true
	default:
		return common.Address{}, false
	}
}

// CloneState is overridden rather than inherited from ethfee.Hook because
// `m` lives on *this* type, not on ethfee.Hook (unlike Unsupported/
// UnsupportedErr, which ethfee.Hook's own CloneState already preserves
// correctly -- see its doc comment). m itself is never needed post-clone
// (only Track() reads it, and Track() is never called on a cloned pool), so
// this override exists purely so the dynamic type stays *feebps.Hook rather
// than *ethfee.Hook.
func (h *Hook) CloneState() uniswapv4.Hook {
	cloned := *h
	return &cloned
}

var _ uniswapv4.Hook = (*Hook)(nil)
