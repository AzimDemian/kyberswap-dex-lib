package mcapfee

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

// TestTrack_LiveChain calls currentFeeBps(PoolKey) on both real deployed
// addresses in HookAddresses, against real pools that carry them (mainnet
// WETH/0x809d54... for the first, WETH/0xd832fe... for the second). Both
// calls were used to verify the ABI/PoolKey wiring against mainnet before
// writing this package -- both currently return 125 (1.25%).
func TestTrack_LiveChain(t *testing.T) {
	t.Parallel()
	if os.Getenv("CI") != "" {
		t.Skip("Skipping RPC test in CI")
	}

	rpcClient := ethrpc.New("https://ethereum-rpc.publicnode.com").
		SetMulticallContract(common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11"))
	chainID := valueobject.ChainIDEthereum

	cases := []struct {
		hookAddr common.Address
		pairedToken string
	}{
		{HookAddresses[1], "0x809d54df01d0e366a0bfaa6947d5cf9a3ea88aeb"}, // a2dcd7bf...
		{HookAddresses[0], "0xd832fe793dfbb6ec2bc94d4ef1893859f7f5637c"}, // 8a22e2a5...
	}

	for _, tc := range cases {
		pool := &entity.Pool{
			Tokens: []*entity.PoolToken{
				{Address: valueobject.WETHByChainID[chainID]},
				{Address: tc.pairedToken},
			},
			StaticExtra: `{"0x0":[true,false],"fee":0,"tS":200,"hooks":"` + tc.hookAddr.Hex() + `"}`,
		}

		h := New(&uniswapv4.HookParam{
			Cfg:         &uniswapv4.Config{ChainID: chainID},
			Pool:        pool,
			HookAddress: tc.hookAddr,
		})

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
		assert.True(t, tracked.FeeBps.Sign() > 0, "expected a positive fee bps for a real launch pool")
		assert.True(t, tracked.FeeBps.Cmp(big.NewInt(1000)) <= 0,
			"sanity ceiling: fee bps over 10%% (1000) suggests the PoolKey no longer hashes to the right PoolId, got %s", tracked.FeeBps)
	}
}
