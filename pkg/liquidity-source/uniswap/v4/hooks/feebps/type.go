package feebps

import "errors"

// ErrPoolHasNoNativeCurrency is returned when the pool has no
// native/wrapped-native side to charge the fee in.
var ErrPoolHasNoNativeCurrency = errors.New("feebps: pool has no native currency, every swap reverts on-chain")
