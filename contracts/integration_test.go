package contracts

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// Integration test: deploy token + AMM, mint, add liquidity, swap.
// All through the executor as real transactions.

func TestTokenDeployMintTransfer(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	aliceKey, alice := testKey(t, 1)
	_, bob := testKey(t, 2)

	// Fund alice.
	stateDB.SetBalance(alice, 100_000_000)

	// --- Block 1: Deploy token contract ---
	aliceCallerID := callerIDFromAddress(alice)
	tokenCode := BuildTokenContract(aliceCallerID, 1_000_000)
	deployTx := &types.Transaction{
		Version:  1,
		Nonce:    0,
		From:     alice,
		To:       types.ZeroAddress, // contract creation
		GasPrice: 1,
		GasLimit: 100_000,
		Data:     tokenCode,
	}
	signTx(t, deployTx, aliceKey)

	block, receipts, err := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	if err != nil {
		t.Fatal(err)
	}
	assertReceipt(t, receipts[0], true, "deploy token")

	tokenAddr := executor.DeriveContractAddress(alice, 0)
	t.Logf("token deployed at %s (block %d, gas %d)", tokenAddr.Hex(), block.Height, block.GasUsed)

	// Verify contract exists.
	acct := stateDB.GetAccount(tokenAddr)
	if acct == nil || !acct.IsContract() {
		t.Fatal("token contract not found")
	}

	// --- Block 2: Mint 10000 tokens to alice ---
	mintData := PackCallData(FnMint, 10000)
	mintTx := &types.Transaction{
		Version:  1,
		Nonce:    1,
		From:     alice,
		To:       tokenAddr,
		GasPrice: 1,
		GasLimit: 500_000,
		Data:     mintData,
	}
	signTx(t, mintTx, aliceKey)

	_, receipts, err = exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{mintTx})
	if err != nil {
		t.Fatal(err)
	}
	assertReceipt(t, receipts[0], true, "mint tokens")

	// Check token balance via storage.
	aliceTokenBal := readContractSlot(stateDB, tokenAddr, SlotBalanceBase+aliceCallerID)
	if aliceTokenBal != 10000 {
		t.Fatalf("expected alice token balance 10000, got %d", aliceTokenBal)
	}

	totalSupply := readContractSlot(stateDB, tokenAddr, SlotTotalSupply)
	if totalSupply != 10000 {
		t.Fatalf("expected total supply 10000, got %d", totalSupply)
	}
	t.Logf("minted 10000 tokens to alice (supply=%d)", totalSupply)

	// --- Block 3: Transfer 3000 tokens from alice to bob ---
	bobCallerID := callerIDFromAddress(bob)
	transferData := PackCallData(FnTransfer, bobCallerID, 3000)
	transferTx := &types.Transaction{
		Version:  1,
		Nonce:    2,
		From:     alice,
		To:       tokenAddr,
		GasPrice: 1,
		GasLimit: 500_000,
		Data:     transferData,
	}
	signTx(t, transferTx, aliceKey)

	_, receipts, err = exec.ProcessBlock(3, types.ZeroHash, []*types.Transaction{transferTx})
	if err != nil {
		t.Fatal(err)
	}
	assertReceipt(t, receipts[0], true, "transfer tokens")

	aliceTokenBal = readContractSlot(stateDB, tokenAddr, SlotBalanceBase+aliceCallerID)
	bobTokenBal := readContractSlot(stateDB, tokenAddr, SlotBalanceBase+bobCallerID)

	if aliceTokenBal != 7000 {
		t.Fatalf("expected alice 7000, got %d", aliceTokenBal)
	}
	if bobTokenBal != 3000 {
		t.Fatalf("expected bob 3000, got %d", bobTokenBal)
	}
	t.Logf("transferred 3000 tokens: alice=%d bob=%d", aliceTokenBal, bobTokenBal)

	// --- Block 4: Query balance of bob via balanceOf ---
	balanceOfData := PackCallData(FnBalanceOf, bobCallerID)
	balanceOfTx := &types.Transaction{
		Version:  1,
		Nonce:    3,
		From:     alice,
		To:       tokenAddr,
		GasPrice: 1,
		GasLimit: 500_000,
		Data:     balanceOfData,
	}
	signTx(t, balanceOfTx, aliceKey)

	_, receipts, err = exec.ProcessBlock(4, types.ZeroHash, []*types.Transaction{balanceOfTx})
	if err != nil {
		t.Fatal(err)
	}
	assertReceipt(t, receipts[0], true, "balanceOf")

	returnVal := readContractSlot(stateDB, tokenAddr, 200)
	if returnVal != 3000 {
		t.Fatalf("expected balanceOf return 3000, got %d", returnVal)
	}
	t.Logf("balanceOf(bob) = %d", returnVal)
}

func TestAMMEndToEnd(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	aliceKey, alice := testKey(t, 1)
	bobKey, bob := testKey(t, 2)

	stateDB.SetBalance(alice, 100_000_000)
	stateDB.SetBalance(bob, 100_000_000)

	// --- Deploy AMM contract ---
	ammCode := BuildAMMContract(997, 0)
	deployTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice,
		To: types.ZeroAddress, GasPrice: 1, GasLimit: 200_000,
		Data: ammCode,
	}
	signTx(t, deployTx, aliceKey)
	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	assertReceipt(t, receipts[0], true, "deploy AMM")
	ammAddr := executor.DeriveContractAddress(alice, 0)
	t.Logf("AMM deployed at %s", ammAddr.Hex())

	// --- Alice adds liquidity: 10000 A, 20000 B ---
	addLiqData := PackCallData(AMMFnAddLiquidity, 10000, 20000)
	addLiqTx := &types.Transaction{
		Version: 1, Nonce: 1, From: alice,
		To: ammAddr, GasPrice: 1, GasLimit: 500_000,
		Data: addLiqData,
	}
	signTx(t, addLiqTx, aliceKey)
	_, receipts, _ = exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{addLiqTx})
	assertReceipt(t, receipts[0], true, "add liquidity")

	resA := readContractSlot(stateDB, ammAddr, AMMSlotReserveA)
	resB := readContractSlot(stateDB, ammAddr, AMMSlotReserveB)
	lpSupply := readContractSlot(stateDB, ammAddr, AMMSlotLPSupply)
	t.Logf("after addLiquidity: reserveA=%d reserveB=%d lpSupply=%d", resA, resB, lpSupply)

	if resA != 10000 || resB != 20000 {
		t.Fatalf("expected reserves 10000/20000, got %d/%d", resA, resB)
	}

	// --- Bob swaps 1000 A for B (with 0.3% fee) ---
	swapData := PackCallData(AMMFnSwapAForB, 1000)
	swapTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob,
		To: ammAddr, GasPrice: 1, GasLimit: 500_000,
		Data: swapData,
	}
	signTx(t, swapTx, bobKey)
	_, receipts, _ = exec.ProcessBlock(3, types.ZeroHash, []*types.Transaction{swapTx})
	assertReceipt(t, receipts[0], true, "swap A->B")

	amountBOut := readContractSlot(stateDB, ammAddr, 200)
	t.Logf("swap 1000 A -> %d B", amountBOut)
	if amountBOut != 1813 {
		t.Fatalf("expected 1813 B out, got %d", amountBOut)
	}

	resA = readContractSlot(stateDB, ammAddr, AMMSlotReserveA)
	resB = readContractSlot(stateDB, ammAddr, AMMSlotReserveB)
	t.Logf("after swap A->B: reserveA=%d reserveB=%d", resA, resB)

	if resA != 11000 {
		t.Fatalf("expected reserveA 11000, got %d", resA)
	}
	if resB != 18187 {
		t.Fatalf("expected reserveB 18187, got %d", resB)
	}

	// --- Bob swaps 500 B for A (with 0.3% fee) ---
	swapData2 := PackCallData(AMMFnSwapBForA, 500)
	swapTx2 := &types.Transaction{
		Version: 1, Nonce: 1, From: bob,
		To: ammAddr, GasPrice: 1, GasLimit: 500_000,
		Data: swapData2,
	}
	signTx(t, swapTx2, bobKey)
	_, receipts, _ = exec.ProcessBlock(4, types.ZeroHash, []*types.Transaction{swapTx2})
	assertReceipt(t, receipts[0], true, "swap B->A")

	amountAOut := readContractSlot(stateDB, ammAddr, 200)
	t.Logf("swap 500 B -> %d A", amountAOut)

	resA = readContractSlot(stateDB, ammAddr, AMMSlotReserveA)
	resB = readContractSlot(stateDB, ammAddr, AMMSlotReserveB)
	t.Logf("after swap B->A: reserveA=%d reserveB=%d", resA, resB)

	// Verify constant product roughly holds.
	k := resA * resB
	t.Logf("k = %d (original k = 200000000)", k)
	if k < 200_000_000 {
		t.Fatalf("constant product violated: k=%d < 200000000", k)
	}

	// --- Alice removes half liquidity ---
	aliceLP := readContractSlot(stateDB, ammAddr, uint64(AMMSlotLPBalBase)+callerIDFromAddress(alice))
	t.Logf("alice LP balance: %d", aliceLP)

	removeLiqData := PackCallData(AMMFnRemoveLiquidity, aliceLP/2)
	removeLiqTx := &types.Transaction{
		Version: 1, Nonce: 2, From: alice,
		To: ammAddr, GasPrice: 1, GasLimit: 500_000,
		Data: removeLiqData,
	}
	signTx(t, removeLiqTx, aliceKey)
	_, receipts, _ = exec.ProcessBlock(5, types.ZeroHash, []*types.Transaction{removeLiqTx})
	assertReceipt(t, receipts[0], true, "remove liquidity")

	withdrawnA := readContractSlot(stateDB, ammAddr, 200)
	withdrawnB := readContractSlot(stateDB, ammAddr, 201)
	t.Logf("removed liquidity: got %d A + %d B", withdrawnA, withdrawnB)

	resA = readContractSlot(stateDB, ammAddr, AMMSlotReserveA)
	resB = readContractSlot(stateDB, ammAddr, AMMSlotReserveB)
	newLPSupply := readContractSlot(stateDB, ammAddr, AMMSlotLPSupply)
	t.Logf("after remove: reserveA=%d reserveB=%d lpSupply=%d", resA, resB, newLPSupply)

	if newLPSupply != aliceLP/2 {
		t.Fatalf("expected LP supply %d, got %d", aliceLP/2, newLPSupply)
	}
}

func TestMultipleSwapsSlippage(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	lpKey, lp := testKey(t, 1)
	traderKey, trader := testKey(t, 2)

	stateDB.SetBalance(lp, 100_000_000)
	stateDB.SetBalance(trader, 100_000_000)

	// Deploy AMM.
	ammCode := BuildAMMContract(997, 0)
	deployTx := &types.Transaction{
		Version: 1, Nonce: 0, From: lp,
		To: types.ZeroAddress, GasPrice: 1, GasLimit: 200_000,
		Data: ammCode,
	}
	signTx(t, deployTx, lpKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	ammAddr := executor.DeriveContractAddress(lp, 0)

	// Add large liquidity pool: 1M / 1M.
	addLiqTx := &types.Transaction{
		Version: 1, Nonce: 1, From: lp,
		To: ammAddr, GasPrice: 1, GasLimit: 500_000,
		Data: PackCallData(AMMFnAddLiquidity, 1_000_000, 1_000_000),
	}
	signTx(t, addLiqTx, lpKey)
	exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{addLiqTx})

	// Execute 10 sequential swaps of 1000 A each and track price impact.
	t.Logf("%-5s %-10s %-10s %-10s %-10s", "Swap", "In(A)", "Out(B)", "ResA", "ResB")
	traderNonce := uint64(0)
	blockHeight := uint64(3)

	totalBOut := uint64(0)
	for i := 0; i < 10; i++ {
		swapTx := &types.Transaction{
			Version: 1, Nonce: traderNonce, From: trader,
			To: ammAddr, GasPrice: 1, GasLimit: 500_000,
			Data: PackCallData(AMMFnSwapAForB, 1000),
		}
		signTx(t, swapTx, traderKey)
		_, receipts, _ := exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{swapTx})
		assertReceipt(t, receipts[0], true, fmt.Sprintf("swap %d", i+1))

		bOut := readContractSlot(stateDB, ammAddr, 200)
		resA := readContractSlot(stateDB, ammAddr, AMMSlotReserveA)
		resB := readContractSlot(stateDB, ammAddr, AMMSlotReserveB)

		t.Logf("%-5d %-10d %-10d %-10d %-10d", i+1, 1000, bOut, resA, resB)
		totalBOut += bOut

		traderNonce++
		blockHeight++
	}

	t.Logf("total: 10000 A in, %d B out (avg price: %.4f B/A)", totalBOut, float64(totalBOut)/10000.0)

	resA := readContractSlot(stateDB, ammAddr, AMMSlotReserveA)
	resB := readContractSlot(stateDB, ammAddr, AMMSlotReserveB)
	k := resA * resB
	t.Logf("final: reserveA=%d reserveB=%d k=%d (original=1000000000000)", resA, resB, k)

	if k < 1_000_000*1_000_000 {
		t.Fatalf("constant product violated: k=%d", k)
	}
}

// --- helpers ---

func assertReceipt(t *testing.T, r *executor.Receipt, expectSuccess bool, label string) {
	t.Helper()
	if r.Success != expectSuccess {
		if expectSuccess {
			t.Fatalf("%s: expected success, got error: %s", label, r.Err)
		} else {
			t.Fatalf("%s: expected failure, got success", label)
		}
	}
}

func callerIDFromAddress(addr types.Address) uint64 {
	return binary.BigEndian.Uint64(addr[12:20])
}

func readContractSlot(stateDB *state.StateDB, contract types.Address, slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	val := stateDB.GetStorage(contract, key)
	return binary.BigEndian.Uint64(val[24:])
}
