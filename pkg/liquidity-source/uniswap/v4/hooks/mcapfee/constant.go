package mcapfee

import "github.com/ethereum/go-ethereum/common"

// HookAddresses lists deployments of an unverified market-cap-tiered
// launchpad fee hook (source is NOT verified on Sourcify/Etherscan for
// either address). Identified by function-selector fingerprinting of the
// runtime bytecode plus live eth_call probes against real pools using
// each address, both cross-checked against the static pool list in
// horadrim-solver-go's pools/ethereum/uniswapv4_pools.json:
//
//   - Full standard v4 hook callback surface (before/afterInitialize,
//     before/afterAddLiquidity, before/afterRemoveLiquidity, before/afterSwap,
//     before/afterDonate) plus owner()/transferOwnership(address) — ordinary
//     OZ-style Ownable, no ownership-handover pattern.
//   - currentFeeBps((address,address,uint24,int24,address)) -- same name and
//     signature as FeeHookV3's own getter (see ../feehookv3) -- confirmed live
//     via eth_call against a real pool for each address below, both currently
//     returning 125 (1.25%).
//   - registerCreator(address,address), creatorOf(address), claim(),
//     protocolWallet(), TIER1_BPS()..TIER4_BPS(), CREATOR_BPS(),
//     PROTOCOL_BPS(), BPS_DENOM(), currentMcapEth(...): a creator-revenue-share,
//     market-cap-tiered fee schedule -- currentFeeBps already folds all of
//     that into one number, so none of it needs reimplementing here.
//
// Deliberately excluded: this hook family is NOT the same deployment as
// FeeHookV3 (different bytecode hash, unverified source) and is kept in its
// own address list rather than folded into feehookv3.HookAddresses --
// matching the getter name alone is not proof of matching behavior (see
// ../lpfeepips's constant.go for the same reasoning applied to a case that
// went the other way).
//
// Critically, and unlike the excluded lpfeepips look-alikes: neither address
// exposes an owner-gated arbitrary-call `execute(address,uint256,bytes)`
// escape hatch, or any other admin surface beyond ordinary fee/ownership
// getters -- the fingerprint the lpfeepips package documents as belonging to
// mass-produced honeypot/scam-token factories, not a fee schedule worth
// pricing.
var HookAddresses = []common.Address{
	common.HexToAddress("0x8a22e2A5768c72751F2Da3E3B904365203B100Cc"), // ethereum
	common.HexToAddress("0xA2dCD7Bf7Ff3c014A855bF00799ccF07E6c800Cc"), // ethereum
}
