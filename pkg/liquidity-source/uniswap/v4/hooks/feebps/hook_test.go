package feebps

import (
	"context"
	"math/big"
	"os"
	"testing"

	"github.com/KyberNetwork/ethrpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
	uniswapv4 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v4"
	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

// TestTrack_LiveChain calls both real deployed method-name variants against
// real pools that carry them. Both currently return a normalized 100 (1%)
// after the SPLIT_DENOM/BPS_DENOM rescale -- this exact call was used to
// verify the ABI wiring against mainnet before writing this package.
func TestTrack_LiveChain(t *testing.T) {
	t.Parallel()
	if os.Getenv("CI") != "" {
		t.Skip("Skipping RPC test in CI")
	}

	rpcClient := ethrpc.New("https://ethereum-rpc.publicnode.com").
		SetMulticallContract(common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11"))
	chainID := valueobject.ChainIDEthereum

	cases := []struct {
		hookAddr    common.Address
		pairedToken string
	}{
		{SplitDenomAddresses[0], "0x00763db966274fb193cc10d504202de2fca7c27b"},
		{BpsDenomAddresses[0], "0x8b67fa312d620e7a5ea902027d8a906222a2ed3d"},
	}

	for _, tc := range cases {
		pool := &entity.Pool{
			Tokens: []*entity.PoolToken{
				{Address: valueobject.WETHByChainID[chainID]},
				{Address: tc.pairedToken},
			},
			StaticExtra: `{"0x0":[true,false],"fee":0,"tS":200,"hooks":"` + tc.hookAddr.Hex() + `"}`,
		}

		h := newFactory(methodsFor(t, tc.hookAddr))(&uniswapv4.HookParam{
			Cfg:         &uniswapv4.Config{ChainID: chainID},
			Pool:        pool,
			HookAddress: tc.hookAddr,
		}).(*Hook)

		raw, err := h.Track(context.Background(), &uniswapv4.HookParam{
			Cfg:         &uniswapv4.Config{ChainID: chainID},
			RpcClient:   rpcClient,
			Pool:        pool,
			HookAddress: tc.hookAddr,
		})
		require.NoError(t, err)

		var tracked Hook
		require.NoError(t, json.Unmarshal(raw, &tracked))
		require.NotNil(t, tracked.FeeBps)
		assert.True(t, tracked.FeeBps.Sign() > 0, "expected a positive fee bps")
		assert.True(t, tracked.FeeBps.Cmp(big.NewInt(1000)) <= 0,
			"sanity ceiling: fee bps over 10%% (1000) is suspicious, got %s", tracked.FeeBps)
	}
}

// TestTrack_GraduatedPoolIsFeeFree is a regression test for a real bug this
// package shipped with: SplitDenomAddresses's FeeHook deployment
// permanently zeroes its fee once a launch token's market cap crosses
// graduationMcap (see constant.go), but feeBps()/SPLIT_DENOM() keep
// reporting the pre-graduation rate forever -- they're global constants,
// not per-pool. Track() must check graduated(token) BEFORE trusting them.
// This pool (WETH/0x00916e04f3a6e9d6b34af20ba9259432a9c2e114) is graduated
// on mainnet as of the time this test was written; feeBps() alone still
// returns 100 for it, so this fails if the graduated() check is ever
// removed or short-circuited.
func TestTrack_GraduatedPoolIsFeeFree(t *testing.T) {
	t.Parallel()
	if os.Getenv("CI") != "" {
		t.Skip("Skipping RPC test in CI")
	}

	rpcClient := ethrpc.New("https://ethereum-rpc.publicnode.com").
		SetMulticallContract(common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11"))
	chainID := valueobject.ChainIDEthereum
	hookAddr := SplitDenomAddresses[0]

	pool := &entity.Pool{
		Tokens: []*entity.PoolToken{
			{Address: valueobject.WETHByChainID[chainID]},
			{Address: "0x00916e04f3a6e9d6b34af20ba9259432a9c2e114"},
		},
		StaticExtra: `{"0x0":[true,false],"fee":0,"tS":200,"hooks":"` + hookAddr.Hex() + `"}`,
	}

	h := newFactory(splitDenomMethods)(&uniswapv4.HookParam{
		Cfg: &uniswapv4.Config{ChainID: chainID}, Pool: pool, HookAddress: hookAddr,
	}).(*Hook)

	raw, err := h.Track(context.Background(), &uniswapv4.HookParam{
		Cfg: &uniswapv4.Config{ChainID: chainID}, RpcClient: rpcClient, Pool: pool, HookAddress: hookAddr,
	})
	require.NoError(t, err)

	var tracked Hook
	require.NoError(t, json.Unmarshal(raw, &tracked))
	require.NotNil(t, tracked.FeeBps)
	assert.Equal(t, big.NewInt(0), tracked.FeeBps, "graduated pool must be priced fee-free, not at the pre-graduation feeBps()")
}

// methodsFor picks the method-name pair matching which address list tc
// belongs to, so the test doesn't need to duplicate constant.go's mapping.
func methodsFor(t *testing.T, addr common.Address) methods {
	t.Helper()
	for _, a := range SplitDenomAddresses {
		if a == addr {
			return splitDenomMethods
		}
	}
	for _, a := range BpsDenomAddresses {
		if a == addr {
			return bpsDenomMethods
		}
	}
	t.Fatalf("address %s not found in either method-name list", addr)
	return methods{}
}
