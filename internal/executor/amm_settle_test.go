package executor

import (
	"testing"

	"github.com/djkazic/tylium/contracts"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// TestAMMSettlementSwapAForB verifies end-to-end that swapAForB:
// - debits tokenA from caller
// - credits tyBTC to caller
func TestAMMSettlementSwapAForB(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 1)
	stateDB.SetBalance(alice, 100_000_000) // 100M tyBTC

	// Deploy token contract (poop).
	ownerID := alice.CallerID()
	tokenCode := contracts.BuildTokenContract(ownerID, 0) // unlimited supply
	deployTokenTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: types.ZeroAddress,
		GasPrice: 1, GasLimit: 200_000, Data: tokenCode,
	}
	signTx(t, deployTokenTx, aliceKey)
	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTokenTx})
	if !receipts[0].Success {
		t.Fatalf("deploy token failed: %s", receipts[0].Err)
	}
	tokenAddr := DeriveContractAddress(alice, 0)
	t.Logf("token at %s (callerID=%d)", tokenAddr.Hex(), tokenAddr.CallerID())

	// Mint 10M tokens to alice.
	mintData := contracts.PackCallData(contracts.FnMint, 10_000_000)
	mintTx := &types.Transaction{
		Version: 1, Nonce: 1, From: alice, To: tokenAddr,
		GasPrice: 1, GasLimit: 200_000, Data: mintData,
	}
	signTx(t, mintTx, aliceKey)
	_, receipts, _ = exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{mintTx})
	if !receipts[0].Success {
		t.Fatalf("mint failed: %s", receipts[0].Err)
	}

	// Check alice token balance (slot 1000+callerID).
	aliceTokenBal := exec.readStorageUint64(tokenAddr, tokenSlotBalanceBase+alice.CallerID())
	t.Logf("alice token balance after mint: %d", aliceTokenBal)
	if aliceTokenBal != 10_000_000 {
		t.Fatalf("expected 10M tokens, got %d", aliceTokenBal)
	}

	// Deploy AMM with tokenAID = token's callerID.
	ammCode := contracts.BuildAMMContract(997, tokenAddr.CallerID())
	deployAMMTx := &types.Transaction{
		Version: 1, Nonce: 2, From: alice, To: types.ZeroAddress,
		GasPrice: 1, GasLimit: 200_000, Data: ammCode,
	}
	signTx(t, deployAMMTx, aliceKey)
	_, receipts, _ = exec.ProcessBlock(3, types.ZeroHash, []*types.Transaction{deployAMMTx})
	if !receipts[0].Success {
		t.Fatalf("deploy AMM failed: %s", receipts[0].Err)
	}
	ammAddr := DeriveContractAddress(alice, 2)
	t.Logf("AMM at %s (callerID=%d)", ammAddr.Hex(), ammAddr.CallerID())

	// Record balances before addLiquidity.
	aliceTyBTCBefore := stateDB.GetOrCreateAccount(alice).Balance
	t.Logf("alice tyBTC before addLiq: %d", aliceTyBTCBefore)

	// Add liquidity: 1M tokenA, 1M tyBTC.
	addLiqData := contracts.PackCallData(contracts.AMMFnAddLiquidity, 1_000_000, 1_000_000)
	addLiqTx := &types.Transaction{
		Version: 1, Nonce: 3, From: alice, To: ammAddr,
		GasPrice: 1, GasLimit: 500_000, Data: addLiqData,
	}
	signTx(t, addLiqTx, aliceKey)
	_, receipts, _ = exec.ProcessBlock(4, types.ZeroHash, []*types.Transaction{addLiqTx})
	if !receipts[0].Success {
		t.Fatalf("addLiquidity failed: %s", receipts[0].Err)
	}
	t.Logf("addLiquidity gasUsed: %d", receipts[0].GasUsed)

	// Verify post-addLiquidity state.
	aliceTokenAfterLiq := exec.readStorageUint64(tokenAddr, tokenSlotBalanceBase+alice.CallerID())
	ammTokenAfterLiq := exec.readStorageUint64(tokenAddr, tokenSlotBalanceBase+ammAddr.CallerID())
	aliceTyBTCAfterLiq := stateDB.GetOrCreateAccount(alice).Balance
	ammTyBTCAfterLiq := stateDB.GetOrCreateAccount(ammAddr).Balance

	t.Logf("after addLiq:")
	t.Logf("  alice tokens: %d (expected 9M)", aliceTokenAfterLiq)
	t.Logf("  AMM tokens:   %d (expected 1M)", ammTokenAfterLiq)
	t.Logf("  alice tyBTC:  %d", aliceTyBTCAfterLiq)
	t.Logf("  AMM tyBTC:    %d (expected 1M)", ammTyBTCAfterLiq)

	if aliceTokenAfterLiq != 9_000_000 {
		t.Fatalf("expected alice tokens 9M, got %d", aliceTokenAfterLiq)
	}
	if ammTokenAfterLiq != 1_000_000 {
		t.Fatalf("expected AMM tokens 1M, got %d", ammTokenAfterLiq)
	}
	if ammTyBTCAfterLiq != 1_000_000 {
		t.Fatalf("expected AMM tyBTC 1M, got %d", ammTyBTCAfterLiq)
	}

	// Now swap 100K tokenA for tyBTC.
	swapData := contracts.PackCallData(contracts.AMMFnSwapAForB, 100_000)
	swapTx := &types.Transaction{
		Version: 1, Nonce: 4, From: alice, To: ammAddr,
		GasPrice: 1, GasLimit: 500_000, Data: swapData,
	}
	signTx(t, swapTx, aliceKey)
	_, receipts, _ = exec.ProcessBlock(5, types.ZeroHash, []*types.Transaction{swapTx})
	if !receipts[0].Success {
		t.Fatalf("swapAForB failed: %s", receipts[0].Err)
	}
	gasUsed := receipts[0].GasUsed
	t.Logf("swapAForB gasUsed: %d", gasUsed)

	// Check results.
	aliceTokenAfterSwap := exec.readStorageUint64(tokenAddr, tokenSlotBalanceBase+alice.CallerID())
	ammTokenAfterSwap := exec.readStorageUint64(tokenAddr, tokenSlotBalanceBase+ammAddr.CallerID())
	aliceTyBTCAfterSwap := stateDB.GetOrCreateAccount(alice).Balance
	ammTyBTCAfterSwap := stateDB.GetOrCreateAccount(ammAddr).Balance

	t.Logf("after swapAForB(100K):")
	t.Logf("  alice tokens: %d (should be 8.9M)", aliceTokenAfterSwap)
	t.Logf("  AMM tokens:   %d (should be 1.1M)", ammTokenAfterSwap)
	t.Logf("  alice tyBTC:  %d", aliceTyBTCAfterSwap)
	t.Logf("  AMM tyBTC:    %d", ammTyBTCAfterSwap)
	t.Logf("  gas cost:     %d tyBTC", gasUsed)

	// Token debit: alice should lose 100K tokens.
	if aliceTokenAfterSwap != aliceTokenAfterLiq-100_000 {
		t.Fatalf("expected alice tokens %d, got %d", aliceTokenAfterLiq-100_000, aliceTokenAfterSwap)
	}
	if ammTokenAfterSwap != ammTokenAfterLiq+100_000 {
		t.Fatalf("expected AMM tokens %d, got %d", ammTokenAfterLiq+100_000, ammTokenAfterSwap)
	}

	// tyBTC credit: alice should GAIN tyBTC (minus gas).
	// amountBOut = reserveB * (amountIn * fee) / (reserveA * 1000 + amountIn * fee)
	// The exact integer division result from the VM.
	actualBOut := ammTyBTCAfterLiq - ammTyBTCAfterSwap
	t.Logf("  actual tyBTC out: %d", actualBOut)

	expectedAliceTyBTC := aliceTyBTCAfterLiq + actualBOut - gasUsed
	t.Logf("  expected alice tyBTC: %d (actual %d, diff %d)",
		expectedAliceTyBTC, aliceTyBTCAfterSwap, int64(aliceTyBTCAfterSwap)-int64(expectedAliceTyBTC))

	if aliceTyBTCAfterSwap != expectedAliceTyBTC {
		// Check if creditB was even applied.
		noCreditTyBTC := aliceTyBTCAfterLiq - gasUsed
		if aliceTyBTCAfterSwap == noCreditTyBTC {
			t.Fatalf("BUG: creditB was NOT applied! alice tyBTC = %d (exactly initial - gas)", aliceTyBTCAfterSwap)
		}
		t.Fatalf("tyBTC mismatch: expected %d, got %d", expectedAliceTyBTC, aliceTyBTCAfterSwap)
	}

	// Verify alice's tyBTC INCREASED net of gas.
	netTyBTCChange := int64(aliceTyBTCAfterSwap) - int64(aliceTyBTCAfterLiq)
	t.Logf("  net tyBTC change: %+d (out=%d, gas=%d)", netTyBTCChange, actualBOut, gasUsed)
	if netTyBTCChange <= 0 {
		t.Fatalf("expected positive net tyBTC from swap, got %d", netTyBTCChange)
	}
}
