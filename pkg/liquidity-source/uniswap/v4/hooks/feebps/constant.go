package feebps

import "github.com/ethereum/go-ethereum/common"

// Both address lists below were identified by function-selector
// fingerprinting the runtime bytecode plus live eth_call probes against
// real pools using each address (cross-checked against horadrim-solver-go's
// pools/ethereum/uniswapv4_pools.json). Both variants share the same
// overall shape as the ethfee family (see ../ethfee): a flat
// beforeSwapReturnDelta/afterSwapReturnDelta ETH fee, plus visible
// "graduation" bookkeeping (graduated(address), graduationMcap(),
// launchBlockSwapAllowed(address)) that only gates whether a swap executes
// on-chain, not what it costs -- irrelevant to pricing, same as any other
// allowlist/gate this codebase treats as a liveness concern (see
// uniswapv4.UnknownHookUnsafe's doc comment). Neither exposes an
// owner-gated arbitrary-call `execute(address,uint256,bytes)` escape hatch
// -- the honeypot/scam-factory fingerprint ../lpfeepips's constant.go
// documents excluding.
//
// The two lists use different getter names for the identical mechanism, so
// they're wired to two distinct Track() implementations in hook.go rather
// than one -- matching how the naming actually differs on-chain -- but kept
// in one package since the fee math itself is identical.

// SplitDenomAddresses charge a flat fee read via feeBps()/SPLIT_DENOM().
var SplitDenomAddresses = []common.Address{
	common.HexToAddress("0x676e38B66A7547Ff685549E4Ad88134ac62200Cc"), // ethereum
}

// BpsDenomAddresses charge a flat fee read via FEE_BPS()/BPS_DENOM().
var BpsDenomAddresses = []common.Address{
	common.HexToAddress("0xca859C62c62633D2b999C83f56db046d274540Cc"), // ethereum
}
