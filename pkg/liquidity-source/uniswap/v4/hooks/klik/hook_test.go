package klik

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

// TestTrack_LiveChain exercises the real two-step read (factory() on the
// hook, then getFeeTiers() + IFactory.getMarketCap() once the factory
// address is known) against a real mainnet Klik pool (launch token
// 0x6965db0623c03982bf357a82d25f66b361f0d993). Verified manually against
// mainnet before writing this package: factory resolves to
// 0xDE60796060c24638c389eFBD36b6b919805CA655, the tier table matches the
// contract's documented default 17-tier schedule, and the token's market
// cap (~0.7 ETH at the time) falls in the first tier (125 bps).
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
		Address: "0xc388adcdb59eeb95d8123c884640e51e9ebf71cca26b2e2f817cd1c81386bdba",
		Tokens: []*entity.PoolToken{
			{Address: valueobject.WETHByChainID[chainID]},
			{Address: "0x6965db0623c03982bf357a82d25f66b361f0d993"},
		},
	}

	h := New(&uniswapv4.HookParam{
		Cfg:         &uniswapv4.Config{ChainID: chainID},
		Pool:        pool,
		HookAddress: hookAddr,
	})
	require.True(t, h.nativePaired)

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
	assert.True(t, tracked.FeeBps.Sign() > 0)
	assert.True(t, tracked.FeeBps.Cmp(big.NewInt(125)) <= 0, "UniversalKlikHook.MAX_FEE_BPS is 125 (1.25%%), got %s", tracked.FeeBps)
}
