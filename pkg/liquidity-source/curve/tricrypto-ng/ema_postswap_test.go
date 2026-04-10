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

// TestEMA_PostSwapAccuracy waits for a swap on the TriCRV pool, re-fetches state
// at the swap block (where LPT == block.timestamp, so VIEW == cached_oracle exactly),
// then tracks drift over subsequent blocks. Drift should be <10 wei.
func TestEMA_PostSwapAccuracy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client, err := ethclient.DialContext(ctx, liveRPC)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	defer client.Close()

	wsClient, err := ethclient.DialContext(ctx, liveWS)
	if err != nil {
		t.Skipf("cannot connect WS: %v", err)
	}
	defer wsClient.Close()

	pool := common.HexToAddress(testPool)
	numDepCoins := 2

	headers := make(chan *types.Header, 10)
	sub, err := wsClient.SubscribeNewHead(ctx, headers)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	// Phase 1: Wait for a swap (LPT changes between blocks)
	t.Log("=== Phase 1: Waiting for a swap on TriCRV pool ===")

	var prevLPT *uint256.Int
	var swapBlock *big.Int
	var swapTs uint64

	for {
		header := <-headers
		blockNum := header.Number
		lpt := psU256NI(t, client, pool, "last_prices_timestamp", blockNum)

		if prevLPT != nil && lpt.Cmp(prevLPT) != 0 {
			// Swap detected!
			swapBlock = blockNum
			swapTs = header.Time
			t.Logf("SWAP at block %d (ts=%d), LPT changed %s → %s",
				blockNum.Uint64(), swapTs, prevLPT.Dec(), lpt.Dec())

			// Verify LPT == block.timestamp (swap just happened)
			if lpt.Uint64() == swapTs {
				t.Log("LPT == block.timestamp — VIEW returns exact cached_oracle")
			} else {
				t.Logf("LPT=%d != blockTs=%d (diff=%d). Multiple txs in block?",
					lpt.Uint64(), swapTs, int64(swapTs)-int64(lpt.Uint64()))
			}
			break
		}
		prevLPT = lpt
		t.Logf("  block %d: no swap (LPT=%s)", blockNum.Uint64(), lpt.Dec())
	}

	// Phase 2: Fetch state at the swap block (exact cached oracle)
	t.Log("\n=== Phase 2: Fetching exact state at swap block ===")

	oracles := make([]uint256.Int, numDepCoins)
	lastPrices := make([]uint256.Int, numDepCoins)
	priceScales := make([]uint256.Int, numDepCoins)
	for i := 0; i < numDepCoins; i++ {
		oracles[i] = *psU256(t, client, pool, "price_oracle", i, swapBlock)
		lastPrices[i] = *psU256(t, client, pool, "last_prices", i, swapBlock)
		priceScales[i] = *psU256(t, client, pool, "price_scale", i, swapBlock)
	}
	// Fetch raw ma_time from packed_rebalancing_params (lowest 64 bits).
	// The ma_time() getter applies * 694 / 1000 for display; EMA uses the raw value.
	packedRP := psU256NI(t, client, pool, "packed_rebalancing_params", swapBlock)
	maTime := new(uint256.Int).And(packedRP, new(uint256.Int).SetUint64(^uint64(0)))

	t.Logf("Oracle[0]=%s Oracle[1]=%s", oracles[0].Dec(), oracles[1].Dec())
	t.Logf("LastPrices[0]=%s LastPrices[1]=%s", lastPrices[0].Dec(), lastPrices[1].Dec())
	t.Logf("RawMaTime=%s, SnapshotTs=%d", maTime.Dec(), swapTs)

	// Phase 3: Track drift over next 15 blocks
	t.Log("\n=== Phase 3: Tracking drift (should be <10 wei per block) ===")

	const trackBlocks = 15
	maxDiff := [2]*uint256.Int{uint256.NewInt(0), uint256.NewInt(0)}
	blocksTracked := 0

	for blocksTracked < trackBlocks {
		header := <-headers
		blockNum := header.Number
		blockTs := header.Time

		// Check for another swap
		currentLPT := psU256NI(t, client, pool, "last_prices_timestamp", blockNum)
		if currentLPT.Uint64() != swapTs {
			t.Logf("  block %d: NEW SWAP (LPT changed). Re-fetching.", blockNum.Uint64())
			swapBlock = blockNum
			swapTs = blockTs
			for i := 0; i < numDepCoins; i++ {
				oracles[i] = *psU256(t, client, pool, "price_oracle", i, swapBlock)
				lastPrices[i] = *psU256(t, client, pool, "last_prices", i, swapBlock)
				priceScales[i] = *psU256(t, client, pool, "price_scale", i, swapBlock)
			}
			// Verify LPT == blockTs
			newLPT := psU256NI(t, client, pool, "last_prices_timestamp", swapBlock)
			if newLPT.Uint64() == blockTs {
				t.Log("  Re-fetched with LPT == blockTs (exact)")
			}
			blocksTracked++
			continue
		}

		// Compute local EMA from swap block to this block
		dt := int64(blockTs - swapTs)
		if dt <= 0 {
			blocksTracked++
			continue
		}

		dtI := new(int256.Int).SetInt64(dt)
		maI := new(int256.Int).SetUint64(maTime.Uint64())
		exponent := i256.Neg(i256.Div(i256.Mul(dtI, I_1e18), maI))
		alpha, err := _snekmate_wad_exp(exponent)
		require.NoError(t, err)
		oneMinusAlpha := new(uint256.Int).Sub(U_1e18, alpha)

		for k := 0; k < numDepCoins; k++ {
			// Cap last_prices at 2 * price_scale (matching on-chain Vyper)
			cappedLP := new(uint256.Int).Set(&lastPrices[k])
			cap := new(uint256.Int).Lsh(&priceScales[k], 1) // 2 * price_scale
			if cappedLP.Cmp(cap) > 0 {
				cappedLP.Set(cap)
			}
			numerator := new(uint256.Int).Add(
				new(uint256.Int).Mul(cappedLP, oneMinusAlpha),
				new(uint256.Int).Mul(&oracles[k], alpha),
			)
			local := new(uint256.Int).Div(numerator, U_1e18)

			onChain := psU256(t, client, pool, "price_oracle", k, blockNum)

			diff := new(uint256.Int)
			sign := " "
			if local.Cmp(onChain) > 0 {
				diff.Sub(local, onChain)
				sign = "+"
			} else {
				diff.Sub(onChain, local)
				sign = "-"
			}

			if diff.Cmp(maxDiff[k]) > 0 {
				maxDiff[k].Set(diff)
			}

			t.Logf("  block %d (dt=%ds) oracle[%d]: diff=%s%s wei",
				blockNum.Uint64(), dt, k, sign, diff.Dec())
		}

		blocksTracked++
	}

	t.Log("\n=== RESULTS ===")
	for k := 0; k < numDepCoins; k++ {
		t.Logf("oracle[%d] max diff: %s wei", k, maxDiff[k].Dec())
	}

	// After a swap where LPT == blockTs, the VIEW == cached_oracle exactly.
	// Subsequent EMA decay should match to within a few wei (integer rounding only).
	threshold := uint256.NewInt(10)
	for k := 0; k < numDepCoins; k++ {
		if maxDiff[k].Cmp(threshold) > 0 {
			t.Errorf("oracle[%d] max diff %s > %s wei threshold — EMA computation doesn't match on-chain",
				k, maxDiff[k].Dec(), threshold.Dec())
		} else {
			t.Logf("oracle[%d] PASS — max diff %s wei <= %s wei threshold",
				k, maxDiff[k].Dec(), threshold.Dec())
		}
	}
}

func psU256(t *testing.T, c *ethclient.Client, addr common.Address, method string, idx int, block *big.Int) *uint256.Int {
	t.Helper()
	a, _ := abi.JSON(strings.NewReader(fmt.Sprintf(`[{"name":"%s","type":"function","inputs":[{"type":"uint256"}],"outputs":[{"type":"uint256"}],"stateMutability":"view"}]`, method)))
	d, _ := a.Pack(method, big.NewInt(int64(idx)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: d}, block)
	require.NoError(t, err, fmt.Sprintf("%s(%d) at block %v", method, idx, block))
	o, _ := a.Unpack(method, r)
	u, _ := uint256.FromBig(o[0].(*big.Int))
	return u
}

func psU256NI(t *testing.T, c *ethclient.Client, addr common.Address, method string, block *big.Int) *uint256.Int {
	t.Helper()
	a, _ := abi.JSON(strings.NewReader(fmt.Sprintf(`[{"name":"%s","type":"function","inputs":[],"outputs":[{"type":"uint256"}],"stateMutability":"view"}]`, method)))
	d, _ := a.Pack(method)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: d}, block)
	require.NoError(t, err, fmt.Sprintf("%s() at block %v", method, block))
	o, _ := a.Unpack(method, r)
	u, _ := uint256.FromBig(o[0].(*big.Int))
	return u
}
