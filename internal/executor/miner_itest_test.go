package executor

import (
	"context"
	"testing"
	"time"

	"github.com/djkazic/tylium/contracts"
	"github.com/djkazic/tylium/internal/miner"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// TestMinerCollectsGasFromTransfer verifies that the miner's coinbase address
// receives gas fees from a simple value transfer.
func TestMinerCollectsGasFromTransfer(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	minerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	minerAddr := minerKey.Public().Address()

	exec := New(stateDB)
	exec.SetCoinbase(minerAddr)

	senderKey, sender := testKey(t, 1)
	_, recipient := testKey(t, 2)
	stateDB.SetBalance(sender, 1_000_000)

	tx := &types.Transaction{
		Version: 1, Nonce: 0, From: sender, To: recipient,
		Value: 500, GasPrice: 10, GasLimit: TxBaseGas,
	}
	signTx(t, tx, senderKey)

	block, receipts, err := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}
	if !receipts[0].Success {
		t.Fatalf("expected success: %s", receipts[0].Err)
	}

	// Miner should receive gasUsed * gasPrice.
	expectedFee := TxBaseGas * tx.GasPrice
	minerBal := stateDB.GetBalance(minerAddr)
	if minerBal != expectedFee {
		t.Fatalf("miner balance: expected %d, got %d", expectedFee, minerBal)
	}

	// Sender should have: initial - value - gasFee.
	senderBal := stateDB.GetBalance(sender)
	expectedSender := uint64(1_000_000) - 500 - expectedFee
	if senderBal != expectedSender {
		t.Fatalf("sender balance: expected %d, got %d", expectedSender, senderBal)
	}

	if block.Coinbase != minerAddr {
		t.Fatalf("block coinbase mismatch")
	}
}

// TestMinerCollectsGasFromFailedTx verifies that the miner collects gas even
// when a contract call reverts.
func TestMinerCollectsGasFromFailedTx(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	minerKey, _ := crypto.GenerateKey()
	minerAddr := minerKey.Public().Address()

	exec := New(stateDB)
	exec.SetCoinbase(minerAddr)

	aliceKey, alice := testKey(t, 1)
	stateDB.SetBalance(alice, 10_000_000)

	// Deploy a token contract.
	aliceID := alice.CallerID()
	tokenCode := contracts.BuildTokenContract(aliceID, 100) // max supply 100
	deployTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: types.ZeroAddress,
		GasPrice: 1, GasLimit: 100_000, Data: tokenCode,
	}
	signTx(t, deployTx, aliceKey)

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	if !receipts[0].Success {
		t.Fatalf("deploy failed: %s", receipts[0].Err)
	}
	deployGasFee := receipts[0].GasUsed * deployTx.GasPrice

	tokenAddr := DeriveContractAddress(alice, 0)

	// Mint 100 (at cap) — should succeed.
	mintTx := &types.Transaction{
		Version: 1, Nonce: 1, From: alice, To: tokenAddr,
		GasPrice: 5, GasLimit: 200_000, Data: packCallData(contracts.FnMint, 100),
	}
	signTx(t, mintTx, aliceKey)

	_, receipts, _ = exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{mintTx})
	if !receipts[0].Success {
		t.Fatalf("mint failed: %s", receipts[0].Err)
	}
	mintGasFee := receipts[0].GasUsed * mintTx.GasPrice

	// Mint 1 more — should FAIL (exceeds max supply), but miner still collects gas.
	failMintTx := &types.Transaction{
		Version: 1, Nonce: 2, From: alice, To: tokenAddr,
		GasPrice: 3, GasLimit: 200_000, Data: packCallData(contracts.FnMint, 1),
	}
	signTx(t, failMintTx, aliceKey)

	_, receipts, _ = exec.ProcessBlock(3, types.ZeroHash, []*types.Transaction{failMintTx})
	if receipts[0].Success {
		t.Fatal("expected mint to fail (exceeds max supply)")
	}
	if receipts[0].GasUsed == 0 {
		t.Fatal("failed tx should still consume gas")
	}
	failGasFee := receipts[0].GasUsed * failMintTx.GasPrice

	// Miner should have accumulated fees from all three blocks.
	expectedTotal := deployGasFee + mintGasFee + failGasFee
	minerBal := stateDB.GetBalance(minerAddr)
	if minerBal != expectedTotal {
		t.Fatalf("miner balance: expected %d, got %d", expectedTotal, minerBal)
	}
}

// TestMinerCollectsGasMultipleTxs verifies gas collection across multiple
// transactions in a single block with varying gas prices.
func TestMinerCollectsGasMultipleTxs(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	minerKey, _ := crypto.GenerateKey()
	minerAddr := minerKey.Public().Address()

	exec := New(stateDB)
	exec.SetCoinbase(minerAddr)

	aliceKey, alice := testKey(t, 1)
	bobKey, bob := testKey(t, 2)
	_, charlie := testKey(t, 3)
	_, dave := testKey(t, 4)
	stateDB.SetBalance(alice, 10_000_000)
	stateDB.SetBalance(bob, 10_000_000)

	txAlice := &types.Transaction{
		Version: 1, Nonce: 0, From: alice,
		To: charlie,
		Value: 100, GasPrice: 10, GasLimit: TxBaseGas,
	}
	signTx(t, txAlice, aliceKey)

	txBob := &types.Transaction{
		Version: 1, Nonce: 0, From: bob,
		To: dave,
		Value: 200, GasPrice: 50, GasLimit: TxBaseGas,
	}
	signTx(t, txBob, bobKey)

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{txAlice, txBob})

	var totalFees uint64
	txs := []*types.Transaction{txAlice, txBob}
	for i, r := range receipts {
		if !r.Success {
			t.Fatalf("tx %d failed: %s", i, r.Err)
		}
		totalFees += r.GasUsed * txs[i].GasPrice
	}

	minerBal := stateDB.GetBalance(minerAddr)
	if minerBal != totalFees {
		t.Fatalf("miner balance: expected %d, got %d", totalFees, minerBal)
	}
}

// TestNoCoinbaseMeansGasBurned verifies that without a coinbase, gas fees are
// burned (no one receives them).
func TestNoCoinbaseMeansGasBurned(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)
	// No SetCoinbase — default zero address.

	senderKey, sender := testKey(t, 1)
	_, recipient := testKey(t, 2)
	stateDB.SetBalance(sender, 1_000_000)

	tx := &types.Transaction{
		Version: 1, Nonce: 0, From: sender,
		To: recipient,
		Value: 100, GasPrice: 10, GasLimit: TxBaseGas,
	}
	signTx(t, tx, senderKey)

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})
	if !receipts[0].Success {
		t.Fatalf("tx failed: %s", receipts[0].Err)
	}

	// Sender pays gas but no one receives it.
	senderBal := stateDB.GetBalance(sender)
	expected := uint64(1_000_000) - 100 - TxBaseGas*10
	if senderBal != expected {
		t.Fatalf("sender balance: expected %d, got %d", expected, senderBal)
	}

	// Zero address should NOT have any balance (gas is burned).
	zeroBal := stateDB.GetBalance(types.ZeroAddress)
	if zeroBal != 0 {
		t.Fatalf("zero address should have 0 balance, got %d", zeroBal)
	}
}

// TestDepositNoGasToMiner verifies that L1 deposits don't charge gas
// (miner gets nothing from deposits).
func TestDepositNoGasToMiner(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	minerKey, _ := crypto.GenerateKey()
	minerAddr := minerKey.Public().Address()

	exec := New(stateDB)
	exec.SetCoinbase(minerAddr)

	_, recipient := testKey(t, 1)

	// Deposit: From == ZeroAddress signals L1 deposit — no signature needed.
	depositTx := &types.Transaction{
		Version: 1,
		From:    types.ZeroAddress,
		To:      recipient,
		Value:   1_000_000,
	}

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{depositTx})
	if !receipts[0].Success {
		t.Fatalf("deposit failed: %s", receipts[0].Err)
	}
	if receipts[0].GasUsed != 0 {
		t.Fatalf("deposit should use 0 gas, got %d", receipts[0].GasUsed)
	}

	// Miner should have 0 — deposits are free.
	minerBal := stateDB.GetBalance(minerAddr)
	if minerBal != 0 {
		t.Fatalf("miner should have 0 from deposit, got %d", minerBal)
	}

	// Recipient should have the full deposit.
	recipientBal := stateDB.GetBalance(recipient)
	if recipientBal != 1_000_000 {
		t.Fatalf("recipient balance: expected 1000000, got %d", recipientBal)
	}
}

// TestMinerAttestationAndGasEndToEnd is a full integration test:
// mine an attestation checkpoint, process transactions, verify gas collection.
func TestMinerAttestationAndGasEndToEnd(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	minerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	minerAddr := minerKey.Public().Address()

	exec := New(stateDB)
	exec.SetCoinbase(minerAddr)

	// Step 1: Deposit funds via L1 bridge — no signature needed.
	aliceKey, alice := testKey(t, 0xAA)
	depositTx := &types.Transaction{
		Version: 1, From: types.ZeroAddress, To: alice, Value: 10_000_000,
	}
	block1, _, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{depositTx})

	// Step 2: Mine an attestation on the state after deposit.
	m := miner.New(minerKey)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cp, err := m.Mine(ctx, block1.StateRoot, types.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if cp.StateRoot != block1.StateRoot {
		t.Fatal("checkpoint state root mismatch")
	}

	// Step 3: Alice sends a transfer — miner should collect gas.
	_, bob := testKey(t, 0xBB)
	transferTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: bob,
		Value: 1000, GasPrice: 5, GasLimit: TxBaseGas,
	}
	signTx(t, transferTx, aliceKey)

	block2, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{transferTx})
	if !receipts[0].Success {
		t.Fatalf("transfer failed: %s", receipts[0].Err)
	}

	expectedFee := TxBaseGas * uint64(5)
	minerBal := stateDB.GetBalance(minerAddr)
	if minerBal != expectedFee {
		t.Fatalf("miner balance: expected %d, got %d", expectedFee, minerBal)
	}

	// Step 4: Mine another attestation on new state.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()

	cp2, err := m.Mine(ctx2, block2.StateRoot, cp.AttestationHash)
	if err != nil {
		t.Fatal(err)
	}
	if cp2.StateRoot != block2.StateRoot {
		t.Fatal("second checkpoint state root mismatch")
	}
	if cp2.PrevChecksum != cp.AttestationHash {
		t.Fatal("checkpoint chain broken")
	}

	// Verify the miner's attestation is valid: H(nonce||stateRoot) == attestationHash.
	var preimage []byte
	preimage = append(preimage, cp2.MinerNonce[:]...)
	preimage = append(preimage, block2.StateRoot[:]...)
	expectedHash := types.Sha256(preimage)
	if cp2.AttestationHash != expectedHash {
		t.Fatal("attestation hash invalid")
	}

	// Verify balances are consistent.
	aliceBal := stateDB.GetBalance(alice)
	bobBal := stateDB.GetBalance(bob)
	expectedAlice := uint64(10_000_000) - 1000 - expectedFee
	if aliceBal != expectedAlice {
		t.Fatalf("alice balance: expected %d, got %d", expectedAlice, aliceBal)
	}
	if bobBal != 1000 {
		t.Fatalf("bob balance: expected 1000, got %d", bobBal)
	}

	// Total supply check: deposit = 10M, nothing destroyed.
	totalSupply := aliceBal + bobBal + minerBal
	if totalSupply != 10_000_000 {
		t.Fatalf("supply mismatch: expected 10000000, got %d", totalSupply)
	}
}
