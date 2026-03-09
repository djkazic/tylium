package executor

import (
	"testing"

	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

func TestProcessEmptyBlock(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	block, receipts, err := exec.ProcessBlock(1, types.ZeroHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if block.Height != 1 {
		t.Fatalf("expected height 1, got %d", block.Height)
	}
	if len(receipts) != 0 {
		t.Fatalf("expected 0 receipts, got %d", len(receipts))
	}
	if block.GasUsed != 0 {
		t.Fatalf("expected 0 gas, got %d", block.GasUsed)
	}
}

func TestSimpleTransfer(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	senderKey, sender := testKey(t, 1)
	stateDB.SetBalance(sender, 1_000_000)

	_, recipient := testKey(t, 2)

	tx := &types.Transaction{
		Version:  1,
		Nonce:    0,
		From:     sender,
		To:       recipient,
		Value:    500,
		GasPrice: 1,
		GasLimit: TxBaseGas,
	}
	signTx(t, tx, senderKey)

	exec := New(stateDB)
	block, receipts, err := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}

	if len(receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(receipts))
	}
	if !receipts[0].Success {
		t.Fatalf("expected success, got error: %s", receipts[0].Err)
	}
	if block.GasUsed != TxBaseGas {
		t.Fatalf("expected %d gas, got %d", TxBaseGas, block.GasUsed)
	}

	// Check balances.
	recipientBal := stateDB.GetBalance(recipient)
	if recipientBal != 500 {
		t.Fatalf("expected recipient balance 500, got %d", recipientBal)
	}
}

func TestInsufficientBalance(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	senderKey, sender := testKey(t, 1)
	stateDB.SetBalance(sender, 100) // not enough

	_, recipient := testKey(t, 2)

	tx := &types.Transaction{
		Version:  1,
		Nonce:    0,
		From:     sender,
		To:       recipient,
		Value:    500,
		GasPrice: 1,
		GasLimit: TxBaseGas,
	}
	signTx(t, tx, senderKey)

	exec := New(stateDB)
	_, receipts, err := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}

	if receipts[0].Success {
		t.Fatal("expected failure for insufficient balance")
	}
}

func TestNonceMismatch(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	senderKey, sender := testKey(t, 1)
	stateDB.SetBalance(sender, 1_000_000)

	_, recipient := testKey(t, 2)

	tx := &types.Transaction{
		Version:  1,
		Nonce:    5, // should be 0
		From:     sender,
		To:       recipient,
		Value:    100,
		GasPrice: 1,
		GasLimit: TxBaseGas,
	}
	signTx(t, tx, senderKey)

	exec := New(stateDB)
	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})
	if receipts[0].Success {
		t.Fatal("expected nonce mismatch failure")
	}
}

func TestDeterministicOrdering(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	senderKey, sender := testKey(t, 1)
	stateDB.SetBalance(sender, 10_000_000)

	_, recipient1 := testKey(t, 2)
	_, recipient2 := testKey(t, 3)

	// Two txs with different gas prices — higher gas price should execute first.
	tx1 := &types.Transaction{
		Version: 1, Nonce: 0, From: sender,
		To: recipient1,
		Value: 100, GasPrice: 10, GasLimit: TxBaseGas,
	}
	signTx(t, tx1, senderKey)

	tx2 := &types.Transaction{
		Version: 1, Nonce: 0, From: sender,
		To: recipient2,
		Value: 200, GasPrice: 50, GasLimit: TxBaseGas,
	}
	signTx(t, tx2, senderKey)

	exec := New(stateDB)
	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx1, tx2})

	// tx2 (higher gas) should succeed first and use nonce 0.
	// tx1 also expects nonce 0 but it's already consumed => nonce mismatch.
	if !receipts[0].Success {
		t.Fatalf("first tx (high gas) should succeed, got: %s", receipts[0].Err)
	}
	if receipts[1].Success {
		t.Fatal("second tx should fail due to nonce conflict")
	}
}

func TestStateRootChanges(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	root1 := stateDB.Root()

	senderKey, sender := testKey(t, 1)
	stateDB.SetBalance(sender, 1_000_000)

	_, recipient := testKey(t, 2)

	tx := &types.Transaction{
		Version: 1, Nonce: 0, From: sender,
		To: recipient,
		Value: 100, GasPrice: 1, GasLimit: TxBaseGas,
	}
	signTx(t, tx, senderKey)

	block, _, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})

	if block.StateRoot == root1 {
		t.Fatal("state root should change after processing transactions")
	}
	if block.PrevStateRoot == block.StateRoot {
		t.Fatal("prev and current state roots should differ")
	}
}

func TestInvalidSignature(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())

	_, sender := testKey(t, 1)
	wrongKey, _ := testKey(t, 99)
	stateDB.SetBalance(sender, 1_000_000)

	_, recipient := testKey(t, 2)

	tx := &types.Transaction{
		Version:  1,
		Nonce:    0,
		From:     sender,
		To:       recipient,
		Value:    100,
		GasPrice: 1,
		GasLimit: TxBaseGas,
	}
	signTx(t, tx, wrongKey) // sign with wrong key

	exec := New(stateDB)
	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{tx})
	if receipts[0].Success {
		t.Fatal("expected failure for invalid signature")
	}
	if receipts[0].Err != "invalid signature" {
		t.Fatalf("expected 'invalid signature' error, got: %s", receipts[0].Err)
	}
}
