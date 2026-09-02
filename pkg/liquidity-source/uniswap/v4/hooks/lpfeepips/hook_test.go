package lpfeepips

import (
	"context"
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

// TestTrack_LiveChain calls LP_FEE_PIPS()/BASIS_POINTS() on the real
// deployed hook contract. Neither view depends on a specific pool/token
// pair, so the paired token here is a placeholder -- what this test actually
// guards is the RPC batching itself: Track() queues two calls and previously
// finished the request with .Call(), which only accepts exactly one queued
// call (see ethrpc.ErrWrongCallParam) and made Track() fail unconditionally
// for every pool using this hook. Regression test for that bug.
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
		Tokens: []*entity.PoolToken{
			{Address: valueobject.WETHByChainID[chainID]},
			{Address: "0x000000000000000000000000000000000000dead"},
		},
		StaticExtra: `{"0x0":[true,false],"fee":0,"tS":200,"hooks":"` + hookAddr.Hex() + `"}`,
	}

	h := New(&uniswapv4.HookParam{Cfg: &uniswapv4.Config{ChainID: chainID}, Pool: pool, HookAddress: hookAddr})

	raw, err := h.Track(context.Background(), &uniswapv4.HookParam{
		Cfg: &uniswapv4.Config{ChainID: chainID}, RpcClient: rpcClient, Pool: pool, HookAddress: hookAddr,
	})
	require.NoError(t, err)

	var tracked Hook
	require.NoError(t, json.Unmarshal(raw, &tracked))
	require.NotNil(t, tracked.FeeBps)
	assert.True(t, tracked.FeeBps.Sign() >= 0)
}
