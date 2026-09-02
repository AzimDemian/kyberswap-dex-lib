package feehookv3

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

// TestTrack_LiveChain calls the real deployed FeeHookV3 contract's
// currentFeeBps(PoolKey) for a real ETH/token pool that carries it
// (mainnet pool 0xf683825b...4821, launch token
// 0xf209b3d7cf01b726f01f5980d614a764dbf435c9). This exact call was used to
// verify the ABI/PoolKey wiring against mainnet before writing this
// package -- it currently returns 150 (1.5%), the tier for a ~5 ETH market
// cap. Guards against the pool's real fee ever exceeding MAX_FEE_BPS (400,
// i.e. 4%), which would indicate the PoolKey no longer hashes to the right
// PoolId (e.g. because the pool's on-chain LP fee changed).
func TestTrack_LiveChain(t *testing.T) {
	t.Parallel()
	if os.Getenv("CI") != "" {
		t.Skip("Skipping RPC test in CI")
	}

	rpcClient := ethrpc.New("https://ethereum-rpc.publicnode.com").
		SetMulticallContract(common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11"))
	chainID := valueobject.ChainIDEthereum
	hookAddr := HookAddresses[0]

	pool := &entity.Pool{
		Address: "0xf683825b8b04cc2f931345f98e4b18fc2c60345a50ebac6382e55b5f982b4821",
		Tokens: []*entity.PoolToken{
			{Address: valueobject.WETHByChainID[chainID]},
			{Address: "0xf209b3d7cf01b726f01f5980d614a764dbf435c9"},
		},
		StaticExtra: `{"0x0":[true,false],"fee":0,"tS":200,"hooks":"` + hookAddr.Hex() + `"}`,
	}

	h := New(&uniswapv4.HookParam{
		Cfg:         &uniswapv4.Config{ChainID: chainID},
		Pool:        pool,
		HookAddress: hookAddr,
	})

	raw, err := h.Track(context.Background(), &uniswapv4.HookParam{
		Cfg:         &uniswapv4.Config{ChainID: chainID},
		RpcClient:   rpcClient,
		Pool:        pool,
		HookAddress: hookAddr,
	})
	require.NoError(t, err)

	var tracked Hook
	require.NoError(t, json.Unmarshal(raw, &tracked))
	require.NotNil(t, tracked.FeeBps)
	assert.True(t, tracked.FeeBps.Sign() > 0, "expected a positive fee bps for a real launch pool")
	assert.True(t, tracked.FeeBps.Cmp(big.NewInt(400)) <= 0, "FeeHookV3.MAX_FEE_BPS is 400 (4%%), got %s", tracked.FeeBps)
}
