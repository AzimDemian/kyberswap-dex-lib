package feehookv3

import "errors"

// ErrPoolHasNoNativeCurrency mirrors FeeHookV3's unconditional
// `if (Currency.unwrap(key.currency0) != address(0)) revert PoolMustHaveEthAsCurrency0();`
// check at the top of _beforeSwap: it runs on every swap regardless of
// direction, so a pool that carries this hook without a native/wrapped-
// native side reverts on 100% of swaps.
var ErrPoolHasNoNativeCurrency = errors.New("feehookv3: pool has no native currency, every swap reverts on-chain")
