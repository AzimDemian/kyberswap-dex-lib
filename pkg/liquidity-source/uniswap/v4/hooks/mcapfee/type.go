package mcapfee

import "errors"

// ErrPoolHasNoNativeCurrency is returned when the pool has no
// native/wrapped-native side to charge the fee in. FeeHookV3 (whose
// currentFeeBps this hook shares) unconditionally reverts every swap in that
// case, and this hook family is assumed to behave the same way -- see
// ../feehookv3/type.go.
var ErrPoolHasNoNativeCurrency = errors.New("mcapfee: pool has no native currency, every swap reverts on-chain")
