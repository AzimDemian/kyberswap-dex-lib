package lpfeepips

import "errors"

// ErrPoolHasNoNativeCurrency is returned for a pool where neither token is
// native/wrapped-native. Every other observed pool using this hook has the
// native currency on one side, and the ethfee delta model this hook reuses
// only knows how to charge the fee on that side (see
// ethfee.NativeCurrencyIsToken0) -- source isn't verified, so rather than
// guess which side an ETH-less pool would charge (or whether it can swap
// at all), treat it the same way feehookv3 treats its own equivalent
// on-chain guard: refuse to quote instead of risking a wrong price.
var ErrPoolHasNoNativeCurrency = errors.New("lpfeepips: pool has no native currency")
