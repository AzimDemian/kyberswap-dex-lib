package stable

import (
	"github.com/goccy/go-json"
	"github.com/holiman/uint256"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/balancer/v3/base"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/balancer/v3/hooks"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/balancer/v3/math"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/balancer/v3/shared"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/source/pool"
)

var _ = pool.RegisterFactory(DexType, NewPoolSimulator)

func NewPoolSimulator(params pool.FactoryParams) (*base.PoolSimulator, error) {
	entityPool := params.EntityPool
	var extra Extra
	if err := json.Unmarshal([]byte(entityPool.Extra), &extra); err != nil {
		return nil, err
	}

	var staticExtra shared.StaticExtra
	if err := json.Unmarshal([]byte(entityPool.StaticExtra), &staticExtra); err != nil {
		return nil, err
	}

	var hook hooks.IHook
	switch staticExtra.HookType {
	case shared.StableSurgeHookType:
		if extra.isRisky(entityPool, params.ChainID) {
			return nil, shared.ErrUnsupportedHook
		}
		hook = hooks.NewStableSurgeHook(extra.MaxSurgeFeePercentage, extra.SurgeThresholdPercentage)
	}

	return base.NewPoolSimulator(params, extra.Extra, &staticExtra, &PoolSimulator{
		currentAmp: extra.AmplificationParameter,
	}, hook)
}

type PoolSimulator struct {
	currentAmp *uint256.Int
}

func (p *PoolSimulator) BaseGas() int64 {
	return baseGas
}

// OnSwap from https://etherscan.io/address/0xc1d48bb722a22cc6abf19facbe27470f08b3db8c#code#F1#L169
func (p *PoolSimulator) OnSwap(param shared.PoolSwapParams) (*uint256.Int, error) {
	invariant, err := p.computeInvariant(param.BalancesScaled18, shared.RoundDown)
	if err != nil {
		return nil, err
	}

	result, err := lo.Ternary(param.Kind == shared.ExactIn,
		math.StableMath.ComputeOutGivenExactIn, math.StableMath.ComputeInGivenExactOut,
	)(
		p.currentAmp,
		param.BalancesScaled18,
		param.IndexIn,
		param.IndexOut,
		param.AmountGivenScaled18,
		invariant,
	)
	if err != nil {
		return nil, err
	}

	// Mirror on-chain StablePool.ensureBalancesWithinMaxImbalanceRange.
	// For ExactIn:  amountIn is the fee-deducted given amount, amountOut is the result.
	// For ExactOut: amountIn is the result (pre-fee), amountOut is the given amount.
	var amountIn, amountOut *uint256.Int
	if param.Kind == shared.ExactIn {
		amountIn, amountOut = param.AmountGivenScaled18, result
	} else {
		amountIn, amountOut = result, param.AmountGivenScaled18
	}
	if err := checkPostSwapImbalance(param.BalancesScaled18, param.IndexIn, param.IndexOut, amountIn, amountOut); err != nil {
		return nil, err
	}

	return result, nil
}

// checkPostSwapImbalance replicates Balancer v3 StablePool's
// ensureBalancesWithinMaxImbalanceRange using scaled-18 balances.
// Reverts with ErrMaxImbalanceRatioExceeded when max/min >= 10 000.
func checkPostSwapImbalance(balances []*uint256.Int, indexIn, indexOut int, amountIn, amountOut *uint256.Int) error {
	var minBal, maxBal uint256.Int
	first := true

	for i, bal := range balances {
		b := new(uint256.Int).Set(bal)
		switch i {
		case indexIn:
			b.Add(b, amountIn)
		case indexOut:
			if b.Lt(amountOut) {
				return ErrMaxImbalanceRatioExceeded
			}
			b.Sub(b, amountOut)
		}
		if first || b.Lt(&minBal) {
			minBal.Set(b)
		}
		if first || b.Gt(&maxBal) {
			maxBal.Set(b)
		}
		first = false
	}

	if minBal.IsZero() {
		return ErrMaxImbalanceRatioExceeded
	}

	var ratio uint256.Int
	ratio.Div(&maxBal, &minBal)
	if ratio.GtUint64(maxImbalanceRatio - 1) {
		return ErrMaxImbalanceRatioExceeded
	}

	return nil
}

func (p *PoolSimulator) computeInvariant(balancesLiveScaled18 []*uint256.Int, rounding shared.Rounding) (*uint256.Int,
	error) {
	invariant, err := math.StableMath.ComputeInvariant(p.currentAmp, balancesLiveScaled18)
	if err != nil {
		return nil, err
	}

	if invariant.Sign() > 0 && rounding == shared.RoundUp {
		return invariant.AddUint64(invariant, 1), nil
	}

	return invariant, nil
}
