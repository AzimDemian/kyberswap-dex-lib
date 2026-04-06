package tricryptong

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/KyberNetwork/blockchain-toolkit/i256"
	"github.com/KyberNetwork/int256"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

const (
	liveRPC   = "https://ethereum-mainnet.core.chainstack.com/366fbbe52de797f1cfcedab34e304c96"
	liveWS    = "wss://ethereum-mainnet.core.chainstack.com/ws/366fbbe52de797f1cfcedab34e304c96"
	testPool  = "0x4eBdF703948ddCEA3B11f675B4D1Fba9d2414A14" // TriCRV
	testBlocks = 25
)

// TestEMA_LiveExact subscribes to new blocks via WebSocket and at each block:
//   1. Uses block.Timestamp to compute EMA locally with uint256 fixed-point math
//   2. Calls price_oracle() at that exact block number
//   3. Compares — should match within a few wei (rounding only)
//
// This eliminates time.Now() drift and tests pure math accuracy.
func TestEMA_LiveExact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live EMA test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	// HTTP client for eth_call at specific blocks
	httpClient, err := ethclient.DialContext(ctx, liveRPC)
	if err != nil {
		t.Skipf("cannot connect to HTTP RPC: %v", err)
	}
	defer httpClient.Close()

	// WS client for block subscription
	wsClient, err := ethclient.DialContext(ctx, liveWS)
	if err != nil {
		t.Skipf("cannot connect to WebSocket: %v", err)
	}
	defer wsClient.Close()

	pool := common.HexToAddress(testPool)
	numDepCoins := 2

	// Pin ALL initial fetches to the same block for atomicity
	t.Log("=== Fetching initial EMA state ===")

	latestBlock, err := httpClient.BlockByNumber(ctx, nil)
	require.NoError(t, err)
	pinBlock := latestBlock.Number()
	snapshotTs := latestBlock.Time()

	storedOracles := make([]uint256.Int, numDepCoins)
	storedLastPrices := make([]uint256.Int, numDepCoins)
	for i := 0; i < numDepCoins; i++ {
		storedOracles[i] = *rpcU256(t, httpClient, pool, "price_oracle", i, pinBlock)
		storedLastPrices[i] = *rpcU256(t, httpClient, pool, "last_prices", i, pinBlock)
	}
	storedLPT := rpcU256(t, httpClient, pool, "last_prices_timestamp", -1, pinBlock)
	maTime := rpcU256(t, httpClient, pool, "ma_time", -1, pinBlock)

	t.Logf("Pinned to block %d (ts=%d)", pinBlock.Uint64(), snapshotTs)
	t.Logf("Oracle[0]=%s Oracle[1]=%s", storedOracles[0].Dec(), storedOracles[1].Dec())
	t.Logf("LastPrices[0]=%s LastPrices[1]=%s", storedLastPrices[0].Dec(), storedLastPrices[1].Dec())
	t.Logf("LPT=%s MaTime=%s", storedLPT.Dec(), maTime.Dec())

	// Subscribe to new blocks
	headers := make(chan *types.Header, 10)
	sub, err := wsClient.SubscribeNewHead(ctx, headers)
	require.NoError(t, err, "subscribe new head")
	defer sub.Unsubscribe()

	t.Logf("\n=== Listening for %d blocks ===\n", testBlocks)

	maxDiffWei := [2]*uint256.Int{{}, {}}
	blocksProcessed := 0

	for blocksProcessed < testBlocks {
		select {
		case err := <-sub.Err():
			t.Fatalf("subscription error: %v", err)
		case header := <-headers:
			blockNum := header.Number
			blockTs := header.Time

			// Compute EMA locally — decay from snapshot time (when VIEW was fetched)
			localOracle := computeEMAuint256(
				storedOracles[:], storedLastPrices[:],
				snapshotTs, blockTs, maTime,
			)

			// Fetch on-chain price_oracle() at this exact block
			for k := 0; k < numDepCoins; k++ {
				onChain := rpcU256(t, httpClient, pool, "price_oracle", k, blockNum)

				var diff uint256.Int
				if localOracle[k].Cmp(onChain) > 0 {
					diff.Sub(&localOracle[k], onChain)
				} else {
					diff.Sub(onChain, &localOracle[k])
				}

				if maxDiffWei[k] == nil || diff.Cmp(maxDiffWei[k]) > 0 {
					maxDiffWei[k] = new(uint256.Int).Set(&diff)
				}

				t.Logf("block %d (ts=%d) oracle[%d]: local=%s on-chain=%s diff=%s wei",
					blockNum.Uint64(), blockTs, k,
					localOracle[k].Dec(), onChain.Dec(), diff.Dec())

				// Check if a swap happened (lastPricesTimestamp changed)
				currentLPT := rpcU256(t, httpClient, pool, "last_prices_timestamp", -1, blockNum)
				if currentLPT.Cmp(storedLPT) != 0 {
					t.Logf("  *** SWAP DETECTED at block %d: LPT changed %s → %s. Re-fetching state.",
						blockNum.Uint64(), storedLPT.Dec(), currentLPT.Dec())
					// Re-fetch state after swap
					storedLPT.Set(currentLPT)
					snapshotTs = blockTs // VIEW result at this block = correct at blockTs
					for j := 0; j < numDepCoins; j++ {
						storedOracles[j] = *rpcU256(t, httpClient, pool, "price_oracle", j, blockNum)
						storedLastPrices[j] = *rpcU256(t, httpClient, pool, "last_prices", j, blockNum)
					}
					break
				}
			}

			blocksProcessed++
		case <-ctx.Done():
			t.Fatal("timeout waiting for blocks")
		}
	}

	t.Log("\n=== RESULTS ===")
	for k := 0; k < numDepCoins; k++ {
		t.Logf("oracle[%d] max diff: %s wei", k, maxDiffWei[k].Dec())
	}

	// Threshold: 10 wei. The only source of error is integer rounding in wad_exp.
	threshold := uint256.NewInt(10)
	for k := 0; k < numDepCoins; k++ {
		if maxDiffWei[k] != nil && maxDiffWei[k].Cmp(threshold) > 0 {
			t.Errorf("oracle[%d] max diff %s wei exceeds threshold %s wei",
				k, maxDiffWei[k].Dec(), threshold.Dec())
		}
	}
}

// computeEMAuint256 replicates the on-chain price_oracle computation.
// snapshotTs is when storedOracle (VIEW result) was fetched — we decay from there,
// not from lastPricesTs, to avoid double-decay.
func computeEMAuint256(
	storedOracle, lastPrices []uint256.Int,
	snapshotTs, blockTs uint64,
	maTime *uint256.Int,
) []uint256.Int {
	if blockTs <= snapshotTs || maTime == nil || maTime.IsZero() {
		result := make([]uint256.Int, len(storedOracle))
		copy(result, storedOracle)
		return result
	}

	dt := blockTs - snapshotTs

	// exponent = -(dt * ln(2) * 1e18 / maTime) — half-life based EMA
	dtI256 := new(int256.Int).SetUint64(dt)
	maTimeI256 := new(int256.Int).SetUint64(maTime.Uint64())
	exponent := i256.Neg(i256.Div(i256.Mul(dtI256, I_1e18), maTimeI256))

	alpha, err := _snekmate_wad_exp(exponent)
	if err != nil {
		result := make([]uint256.Int, len(storedOracle))
		copy(result, storedOracle)
		return result
	}

	oneMinusAlpha := new(uint256.Int).Sub(U_1e18, alpha)

	result := make([]uint256.Int, len(storedOracle))
	for i := range result {
		// (lastPrices[i] * (1e18 - alpha) + storedOracle[i] * alpha) / 1e18
		// Single division to match on-chain Vyper
		numerator := new(uint256.Int).Add(
			new(uint256.Int).Mul(&lastPrices[i], oneMinusAlpha),
			new(uint256.Int).Mul(&storedOracle[i], alpha),
		)
		result[i].Div(numerator, U_1e18)
	}
	return result
}

// rpcU256 calls a view function returning uint256 at a specific block.
// index < 0 means no argument. blockNum nil means latest.
func rpcU256(t *testing.T, client *ethclient.Client, contract common.Address, method string, index int, blockNum *big.Int) *uint256.Int {
	t.Helper()
	var abiStr string
	if index < 0 {
		abiStr = `[{"name":"` + method + `","type":"function","inputs":[],"outputs":[{"type":"uint256"}],"stateMutability":"view"}]`
	} else {
		abiStr = `[{"name":"` + method + `","type":"function","inputs":[{"type":"uint256"}],"outputs":[{"type":"uint256"}],"stateMutability":"view"}]`
	}
	parsedABI, _ := abi.JSON(strings.NewReader(abiStr))

	var data []byte
	var err error
	if index < 0 {
		data, err = parsedABI.Pack(method)
	} else {
		data, err = parsedABI.Pack(method, big.NewInt(int64(index)))
	}
	require.NoError(t, err, fmt.Sprintf("pack %s", method))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := client.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: data}, blockNum)
	require.NoError(t, err, fmt.Sprintf("call %s at block %v", method, blockNum))

	outputs, err := parsedABI.Unpack(method, result)
	require.NoError(t, err)

	val := outputs[0].(*big.Int)
	u, overflow := uint256.FromBig(val)
	require.False(t, overflow)
	return u
}
