package lpfeepips

import "github.com/ethereum/go-ethereum/common"

// HookAddresses lists deployments identified by function-selector
// fingerprinting (source is NOT verified on Sourcify/Etherscan for either
// address, so this is inferred from bytecode, not read from source):
// beforeSwap/afterSwap/getHookPermissions/poolManager/unlockCallback plus
// exactly LP_FEE_PIPS()/BASIS_POINTS()/TICK_SPACING() and nothing else --
// no owner/admin surface, no arbitrary-call `execute`, no pause switch.
//
// This is deliberately a narrow allowlist, not "every address with a
// similar-looking fee getter". A much larger cluster of addresses expose a
// superficially similar BPS()-style getter but ALSO carry function-selector
// collision decoys (e.g. names like `_SIMONdotBLACK_`, `ideal_warn_timed`,
// `transfer_attention_tg_invmru_*`) plus an owner-gated `execute(address,
// uint256,bytes)` arbitrary-call escape hatch -- a fingerprint associated
// with mass-produced scam/honeypot token factories, not a fee schedule
// worth approximating. Those are intentionally NOT included here; do not
// add addresses to this list by pattern-matching the getter name alone --
// check the full selector set first.
var HookAddresses = []common.Address{
	common.HexToAddress("0x35fe236ea82f7cf525c9719d7df8f49f94d720cc"), // ethereum
	common.HexToAddress("0x90c67c1e866f86526f0e338459cd435e1f23a0cc"), // ethereum
}
