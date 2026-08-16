package uniswapv4

import (
	"fmt"
	"maps"
	"math/big"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/pancake/infinity/shared"
	uniswapv3 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4/hooks/few"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

type PoolSimulator struct {
	*uniswapv3.PoolSimulator
	staticExtra   StaticExtra
	hook          Hook
	chainID       valueobject.ChainID
	tokenWrappers []ITokenWrapper
}

var _ = pool.RegisterFactory1(DexType, NewPoolSimulator)

func NewPoolSimulator(entityPool entity.Pool, chainID valueobject.ChainID) (*PoolSimulator, error) {
	var extra ExtraU256
	if err := json.Unmarshal([]byte(entityPool.Extra), &extra); err != nil {
		return nil, err
	}
	var staticExtra StaticExtra
	if err := json.Unmarshal([]byte(entityPool.StaticExtra), &staticExtra); err != nil {
		return nil, fmt.Errorf("unmarshal static extra: %w", err)
	}

	hook, ok := GetHook(staticExtra.HooksAddress, &HookParam{
		Cfg:       &Config{ChainID: chainID},
		Pool:      &entityPool,
		HookExtra: HookExtra(extra.HookExtra),
	})
	isDynamicFee := shared.IsDynamicFee(staticExtra.Fee)
	// HasSwapPermissions covers the normal before/afterSwap bits; ChangesSwapDeltas
	// is included too since a mined address could in principle set a returns-delta
	// bit without the corresponding before/afterSwap bit (on-chain that flag would
	// then just never fire, but we don't want to rely on that to decide safety).
	if hooksAddr := staticExtra.HooksAddress; !ok &&
		(HasSwapPermissions(hooksAddr) || ChangesSwapDeltas(hooksAddr)) {
		// No registered adapter. Only reject if the hook's permission bits
		// mean BaseHook's zero-delta/zero-fee-override behavior could
		// genuinely misprice the swap (see UnknownHookUnsafe); a hook that
		// can merely observe/revert (allowlist, time gate, accounting, ...)
		// is priced correctly by BaseHook, so let it through instead of
		// dropping the pool entirely.
		unsafe := UnknownHookUnsafe(hooksAddr, isDynamicFee)
		RecordUnknownHook(hooksAddr, !unsafe)
		if unsafe {
			return nil, shared.ErrUnsupportedHook
		}
	}

	allowEmptyTicks := hook.AllowEmptyTicks()

	if isDynamicFee {
		// staticExtra.Fee/entityPool.SwapFee here is PoolKey.fee as read from
		// chain, which for a dynamic-fee pool is LPFeeLibrary.DYNAMIC_FEE_FLAG
		// (0x800000 = 8_388_608), not a real fee -- it's a marker meaning "ask
		// the hook". The underlying v3 math engine has no concept of that
		// marker and validates fee < FeeMax (1_000_000), so passing it through
		// unmodified always fails pool construction with ErrFeeTooHigh. The
		// real per-swap fee is applied later via hook.BeforeSwap's SwapFee
		// override in CalcAmountOut/CalcAmountIn, so 0 here is just a valid
		// placeholder for construction.
		entityPool.SwapFee = 0
	}

	v3PoolSimulator, err := uniswapv3.NewPoolSimulatorWithExtra(entityPool, extra.ExtraTickU256,
		uniswapv3.SimulatorConfig{AllowEmptyTicks: allowEmptyTicks})
	if err != nil {
		return nil, err
	}
	v3PoolSimulator.Gas = defaultGas

	return &PoolSimulator{
		PoolSimulator: v3PoolSimulator,
		staticExtra:   staticExtra,
		hook:          hook,
		chainID:       chainID,
		tokenWrappers: []ITokenWrapper{few.NewTokenWrapper()},
	}, nil
}

func (p *PoolSimulator) CalcAmountOut(param pool.CalcAmountOutParams) (swapResult *pool.CalcAmountOutResult, err error) {
	originalTokenIn, originalTokenOut := param.TokenAmountIn.Token, param.TokenOut
	var wrapAdditionalGas int64
	var beforeSwapResult *BeforeSwapResult
	var afterSwapResult *AfterSwapResult

	defer func() { // modify result before return
		if swapResult == nil {
			return
		}
		v4SwapInfo := SwapInfo{
			PoolSwapInfo: swapResult.SwapInfo.(PoolSwapInfo),
		}

		if swapResult.TokenAmountOut != nil {
			swapResult.TokenAmountOut.Token = originalTokenOut

			if beforeSwapResult != nil {
				swapResult.TokenAmountOut.Amount.Sub(swapResult.TokenAmountOut.Amount, beforeSwapResult.DeltaUnspecified)
				swapResult.Gas += beforeSwapResult.Gas
				v4SwapInfo.HookSwapInfo = beforeSwapResult.SwapInfo
			}

			if afterSwapResult != nil {
				swapResult.TokenAmountOut.Amount.Sub(swapResult.TokenAmountOut.Amount, afterSwapResult.HookFee)
				swapResult.Gas += afterSwapResult.Gas
			}
		}
		swapResult.SwapInfo = v4SwapInfo

		if swapResult.RemainingTokenAmountIn != nil {
			swapResult.RemainingTokenAmountIn.Token = originalTokenIn
		}

		swapResult.Gas += wrapAdditionalGas

		if swapResult.TokenAmountOut.Amount.Sign() < 0 {
			swapResult = nil
			err = ErrInvalidAmountOut
		}
	}()

	// Wrap/unwrap tokens if needed and calculate wrap gas
	if p.GetTokenIndex(param.TokenAmountIn.Token) == -1 {
		for _, wrapper := range p.tokenWrappers {
			metadata, canWrap := wrapper.CanWrap(p.chainID, param.TokenAmountIn.Token)
			if canWrap {
				param.TokenAmountIn.Token = metadata.GetWrapToken()
				wrapAdditionalGas += p.Gas.BaseGas
				break
			}
		}
	}
	if p.GetTokenIndex(param.TokenOut) == -1 {
		for _, wrapper := range p.tokenWrappers {
			metadata, canUnwrap := wrapper.CanWrap(p.chainID, param.TokenOut)
			if canUnwrap {
				param.TokenOut = metadata.GetWrapToken()
				wrapAdditionalGas += p.Gas.BaseGas
				break
			}
		}
	}

	// If no hooks, just do swap
	poolSim := p.PoolSimulator
	if p.hook == nil {
		return p.PoolSimulator.CalcAmountOut(param)
	}

	tokenIn := param.TokenAmountIn.Token
	zeroForOne := p.GetTokenIndex(tokenIn) == 0
	amountIn := new(big.Int).Set(param.TokenAmountIn.Amount)

	if p.hook.CanBeforeSwap(p.staticExtra.HooksAddress) {
		if beforeSwapResult, err = p.hook.BeforeSwap(&BeforeSwapParams{
			CalcOut:         true,
			ZeroForOne:      zeroForOne,
			AmountSpecified: amountIn,
		}); err != nil {
			return nil, fmt.Errorf("[BeforeSwap] %w", err)
		} else if err = ValidateBeforeSwapResult(beforeSwapResult); err != nil {
			return nil, fmt.Errorf("[BeforeSwap] validation failed: %w", err)
		}

		if amountIn.Sub(amountIn, beforeSwapResult.DeltaSpecified).Sign() < 0 {
			return nil, ErrInvalidAmountIn
		}

		if !shared.IsDynamicFee(p.staticExtra.Fee) { // ignore if not dynamic fee
		} else if beforeSwapResult.SwapFee >= FeeMax {
			return nil, ErrInvalidFee
		} else if beforeSwapResult.SwapFee > 0 && beforeSwapResult.SwapFee != p.V3Pool.Fee {
			cloned := *poolSim
			clonedV3Pool := *poolSim.V3Pool
			cloned.V3Pool = &clonedV3Pool
			cloned.V3Pool.Fee = beforeSwapResult.SwapFee
			poolSim = &cloned
		}
	}

	if swapResult, err = poolSim.CalcAmountOut(pool.CalcAmountOutParams{
		TokenAmountIn: pool.TokenAmount{
			Token:  tokenIn,
			Amount: amountIn,
		},
		TokenOut: param.TokenOut,
	}); err != nil {
		return nil, err
	}

	if p.hook.CanAfterSwap(p.staticExtra.HooksAddress) {
		afterSwapResult, err = p.hook.AfterSwap(&AfterSwapParams{
			BeforeSwapParams: &BeforeSwapParams{
				CalcOut:         true,
				ZeroForOne:      zeroForOne,
				AmountSpecified: amountIn,
			},
			AmountIn:  amountIn,
			AmountOut: swapResult.TokenAmountOut.Amount,
		})
		if err != nil {
			return nil, fmt.Errorf("[AfterSwap] %w", err)
		} else if err = ValidateAfterSwapResult(afterSwapResult); err != nil {
			return nil, fmt.Errorf("[AfterSwap] validation failed: %w", err)
		}
	}

	return
}

func (p *PoolSimulator) CalcAmountIn(param pool.CalcAmountInParams) (swapResult *pool.CalcAmountInResult, err error) {
	originalTokenOut, originalTokenIn := param.TokenAmountOut.Token, param.TokenIn
	var wrapAdditionalGas int64
	var beforeSwapResult *BeforeSwapResult
	var afterSwapResult *AfterSwapResult

	defer func() { // modify result before return
		if swapResult == nil {
			return
		}
		v4SwapInfo := SwapInfo{
			PoolSwapInfo: swapResult.SwapInfo.(PoolSwapInfo),
		}

		if swapResult.TokenAmountIn != nil {
			swapResult.TokenAmountIn.Token = originalTokenIn

			if beforeSwapResult != nil {
				swapResult.TokenAmountIn.Amount.Add(swapResult.TokenAmountIn.Amount, beforeSwapResult.DeltaUnspecified)
				swapResult.Gas += beforeSwapResult.Gas
				v4SwapInfo.HookSwapInfo = beforeSwapResult.SwapInfo
			}

			if afterSwapResult != nil {
				swapResult.TokenAmountIn.Amount.Add(swapResult.TokenAmountIn.Amount, afterSwapResult.HookFee)
				swapResult.Gas += afterSwapResult.Gas
			}
		}
		swapResult.SwapInfo = v4SwapInfo

		if swapResult.RemainingTokenAmountOut != nil {
			swapResult.RemainingTokenAmountOut.Token = originalTokenOut
		}

		swapResult.Gas += wrapAdditionalGas

		if swapResult.TokenAmountIn.Amount.Sign() < 0 {
			swapResult = nil
			err = ErrInvalidAmountIn
		}
	}()

	// Wrap/unwrap tokens if needed and calculate wrap gas
	if p.GetTokenIndex(param.TokenAmountOut.Token) == -1 {
		for _, wrapper := range p.tokenWrappers {
			metadata, canWrap := wrapper.CanWrap(p.chainID, param.TokenAmountOut.Token)
			if canWrap {
				param.TokenAmountOut.Token = metadata.GetWrapToken()
				wrapAdditionalGas += p.Gas.BaseGas
				break
			}
		}
	}
	if p.GetTokenIndex(param.TokenIn) == -1 {
		for _, wrapper := range p.tokenWrappers {
			metadata, canUnwrap := wrapper.CanWrap(p.chainID, param.TokenIn)
			if canUnwrap {
				param.TokenIn = metadata.GetWrapToken()
				wrapAdditionalGas += p.Gas.BaseGas
				break
			}
		}
	}

	poolSim := p.PoolSimulator
	if p.hook == nil {
		swapResult, err = poolSim.CalcAmountIn(param)
		return
	}

	tokenOut := param.TokenAmountOut.Token
	zeroForOne := p.GetTokenIndex(tokenOut) == 1
	amountOut := new(big.Int).Set(param.TokenAmountOut.Amount)

	if p.hook.CanBeforeSwap(p.staticExtra.HooksAddress) {
		if beforeSwapResult, err = p.hook.BeforeSwap(&BeforeSwapParams{
			CalcOut:         false,
			ZeroForOne:      zeroForOne,
			AmountSpecified: amountOut,
		}); err != nil {
			return nil, fmt.Errorf("[BeforeSwap] %w", err)
		} else if err = ValidateBeforeSwapResult(beforeSwapResult); err != nil {
			return nil, fmt.Errorf("[BeforeSwap] validation failed: %w", err)
		}

		if amountOut.Add(amountOut, beforeSwapResult.DeltaSpecified).Sign() < 0 {
			return nil, ErrInvalidAmountOut
		}

		if !shared.IsDynamicFee(p.staticExtra.Fee) { // ignore if not dynamic fee
		} else if beforeSwapResult.SwapFee >= FeeMax {
			return nil, ErrInvalidFee
		} else if beforeSwapResult.SwapFee > 0 && beforeSwapResult.SwapFee != p.V3Pool.Fee {
			cloned := *poolSim
			clonedV3Pool := *poolSim.V3Pool
			cloned.V3Pool = &clonedV3Pool
			cloned.V3Pool.Fee = beforeSwapResult.SwapFee
			poolSim = &cloned
		}
	}

	if swapResult, err = poolSim.CalcAmountIn(pool.CalcAmountInParams{
		TokenAmountOut: pool.TokenAmount{
			Token:  tokenOut,
			Amount: amountOut,
		},
		TokenIn: param.TokenIn,
	}); err != nil {
		return nil, err
	}

	if p.hook.CanAfterSwap(p.staticExtra.HooksAddress) {
		if afterSwapResult, err = p.hook.AfterSwap(&AfterSwapParams{
			BeforeSwapParams: &BeforeSwapParams{
				CalcOut:         false,
				ZeroForOne:      zeroForOne,
				AmountSpecified: amountOut,
			},
			AmountIn:  swapResult.TokenAmountIn.Amount,
			AmountOut: amountOut,
		}); err != nil {
			return nil, fmt.Errorf("[AfterSwap] %w", err)
		} else if err = ValidateAfterSwapResult(afterSwapResult); err != nil {
			return nil, fmt.Errorf("[AfterSwap] validation failed: %w", err)
		}
	}

	return
}

func (p *PoolSimulator) CanSwapFrom(address string) []string {
	return p.CanSwapTo(address)
}

func (p *PoolSimulator) CanSwapTo(address string) []string {
	tokenIndex := p.GetTokenIndex(address)

	result := map[string]struct{}{}
	if tokenIndex == -1 {
		for _, wrapper := range p.tokenWrappers {
			metadata, canWrap := wrapper.CanWrap(p.chainID, address)
			if !canWrap {
				continue
			}

			wrapTokenIndex := p.GetTokenIndex(metadata.GetWrapToken())
			if wrapTokenIndex == -1 {
				continue
			}

			res := p.CanSwapTo(metadata.GetWrapToken())
			for _, token := range res {
				result[token] = struct{}{}
			}
		}
	} else { // tokenIndex >= 0
		for _, token := range p.Info.Tokens {
			if token != address {
				result[token] = struct{}{}

				for _, wrapper := range p.tokenWrappers {
					metadata, canUnwrap := wrapper.IsWrapped(p.chainID, token)
					if canUnwrap && metadata.GetUnwrapToken() != address {
						result[metadata.GetUnwrapToken()] = struct{}{}
					}
				}
			}
		}
	}

	return slices.Collect(maps.Keys(result))
}

func (p *PoolSimulator) GetExchange() string {
	return p.hook.GetExchange()
}

// GetHookData returns the hookData this pool's hook expects to be forwarded on
// every PoolManager.swap call. Most hooks don't need any (empty bytes), but
// some (e.g. hooks/cult) require a fixed non-empty payload for the on-chain
// hook contract to behave as quoted here.
func (p *PoolSimulator) GetHookData() []byte {
	return p.hook.GetHookData()
}

func (p *PoolSimulator) GetTokens() []string {
	seen := make(map[string]struct{})
	tokens := make([]string, 0, len(p.Info.Tokens))

	for _, token := range p.Info.Tokens {
		if _, exists := seen[token]; !exists {
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}

		for _, wrapper := range p.tokenWrappers {
			if metadata, isWrapped := wrapper.IsWrapped(p.chainID, token); isWrapped {
				unwrappedToken := metadata.GetUnwrapToken()
				if _, exists := seen[unwrappedToken]; !exists {
					seen[unwrappedToken] = struct{}{}
					tokens = append(tokens, unwrappedToken)
				}
			}
		}
	}

	return tokens
}

func (p *PoolSimulator) CloneState() pool.IPoolSimulator {
	cloned := *p
	cloned.PoolSimulator = p.PoolSimulator.CloneState().(*uniswapv3.PoolSimulator)
	if cloned.hook != nil {
		cloned.hook = p.hook.CloneState()
		if _, ok := cloned.hook.(*BaseHook); ok {
			if _, ok = p.hook.(*BaseHook); !ok {
				cloned.hook = p.hook
			}
		}
	}
	return &cloned
}

func (p *PoolSimulator) UpdateBalance(params pool.UpdateBalanceParams) {
	if params.SwapInfo == nil {
		return
	}
	v4SwapInfo, ok := params.SwapInfo.(SwapInfo)
	if !ok {
		return
	}
	if p.hook != nil {
		p.hook.UpdateBalance(v4SwapInfo.HookSwapInfo)
	}
	params.SwapInfo = v4SwapInfo.PoolSwapInfo
	p.PoolSimulator.UpdateBalance(params)
}

// GetMetaInfo
// adapt from https://github.com/KyberNetwork/kyberswap-dex-lib-private/blob/c1877a8c19759faeb7d82b6902ed335f0657ce3e/pkg/liquidity-source/uniswap-v4/pool_simulator.go#L201
func (p *PoolSimulator) GetMetaInfo(tokenIn string, tokenOut string) any {
	tokenInAfterWrap, tokenOutBeforeUnwrap := tokenIn, tokenOut
	var wrapMetadata *TokenWrapMetadata

	if p.GetTokenIndex(tokenIn) == -1 {
		for _, wrapper := range p.tokenWrappers {
			if metadata, canWrap := wrapper.CanWrap(p.chainID, tokenIn); canWrap {
				tokenInAfterWrap = metadata.GetWrapToken()
				if metadata.IsUnwrapNative() {
					tokenIn = NativeTokenAddress.Hex()
				}

				wrapMetadata = &TokenWrapMetadata{WrapInfo: &WrapInfo{
					TokenIn:     tokenIn,
					TokenOut:    metadata.GetWrapToken(),
					HookAddress: metadata.GetHook(),
					PoolAddress: metadata.GetPool(),
					TickSpacing: metadata.GetTickSpacing(),
					Fee:         metadata.GetFee(),
					HookData:    metadata.GetHookData(),
				}}
				break
			}
		}
	}

	if p.GetTokenIndex(tokenOut) == -1 {
		for _, wrapper := range p.tokenWrappers {
			if metadata, canUnwrap := wrapper.CanWrap(p.chainID, tokenOut); canUnwrap {
				tokenOutBeforeUnwrap = metadata.GetWrapToken()
				if metadata.IsUnwrapNative() {
					tokenOut = NativeTokenAddress.Hex()
				}

				if wrapMetadata == nil {
					wrapMetadata = &TokenWrapMetadata{}
				}
				wrapMetadata.UnwrapInfo = &WrapInfo{
					TokenIn:     tokenOutBeforeUnwrap,
					TokenOut:    tokenOut,
					HookAddress: metadata.GetHook(),
					PoolAddress: metadata.GetPool(),
					TickSpacing: metadata.GetTickSpacing(),
					Fee:         metadata.GetFee(),
					HookData:    metadata.GetHookData(),
				}
			}
		}
	}

	tokenInAddress, tokenOutAddress := NativeTokenAddress, NativeTokenAddress
	if !p.staticExtra.IsNative[p.GetTokenIndex(tokenInAfterWrap)] {
		tokenInAddress = common.HexToAddress(tokenInAfterWrap)
	}
	if !p.staticExtra.IsNative[p.GetTokenIndex(tokenOutBeforeUnwrap)] {
		tokenOutAddress = common.HexToAddress(tokenOutBeforeUnwrap)
	}

	return PoolMetaInfo{
		Router:            p.staticExtra.UniversalRouterAddress,
		Permit2Addr:       p.staticExtra.Permit2Address,
		TokenIn:           tokenInAddress,
		TokenOut:          tokenOutAddress,
		Fee:               p.staticExtra.Fee,
		TickSpacing:       p.staticExtra.TickSpacing,
		HookAddress:       p.staticExtra.HooksAddress,
		HookData:          p.hook.GetHookData(),
		PriceLimit:        p.GetSqrtPriceLimit(tokenInAfterWrap == p.Info.Tokens[0]),
		TokenWrapMetadata: wrapMetadata,
	}
}

func (s *PoolSimulator) SwapReceiveNativeIn(tokenIn, tokenOut string, _ valueobject.ChainID) bool {
	meta := s.GetMetaInfo(tokenIn, tokenOut).(PoolMetaInfo)
	return meta.TokenIn == NativeTokenAddress
}

func (s *PoolSimulator) SwapReturnNativeOut(tokenIn, tokenOut string, _ valueobject.ChainID) bool {
	meta := s.GetMetaInfo(tokenIn, tokenOut).(PoolMetaInfo)
	return meta.TokenOut == NativeTokenAddress
}
