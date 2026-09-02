package klik

import "github.com/ethereum/go-ethereum/common"

// HookAddresses lists known deployments of "UniversalKlikHook" v2 -- the
// current, tier-configurable version with correct exact-in/exact-out
// handling on both sides of the trade. The two addresses below are
// byte-for-byte identical contracts (Sourcify's only diff between them is
// an added `VERSION = "2"` constant).
//
// NOT included: 0xba806a6135c5cd93ecce1258c28f57a8d291e0cc, an older
// UniversalKlikHook deployment with hardcoded tiers and a different
// (buy/sell-direction-based, not specified-side-based) fee model -- it
// needs its own adapter rather than reusing this one. See package doc.
var HookAddresses = []common.Address{
	common.HexToAddress("0x07f17023DB9CeC3f8C6Bb53C6940e29dfFB0A0cc"), // ethereum
	common.HexToAddress("0x96B893683AFBFEc071E90F78D00De4bB932fE0cc"), // ethereum
}
