package executor

import (
	"encoding/binary"
	"testing"

	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

func TestHTLCLockClaimFlow(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)
	stateDB.SetBalance(bob, 1_000_000)

	// Generate a secret and hashlock.
	secret := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	// Alice locks 50000 for Bob, timelock at block 100.
	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 50000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})
	if !receipts[0].Success {
		t.Fatalf("lock failed: %s", receipts[0].Err)
	}

	// Verify HTLC ID returned in slot 200.
	htlcID := readHTLCSlot(stateDB, 200)
	if htlcID != 0 {
		t.Fatalf("expected HTLC ID 0, got %d", htlcID)
	}

	// Verify alice's balance decreased (value + gas).
	aliceBal := stateDB.GetBalance(alice)
	if aliceBal >= 1_000_000 {
		t.Fatalf("alice balance should have decreased, got %d", aliceBal)
	}
	t.Logf("alice balance after lock: %d", aliceBal)

	// Verify system address holds the funds.
	sysBal := stateDB.GetBalance(HTLCSystemAddress)
	if sysBal != 50000 {
		t.Fatalf("system balance should be 50000, got %d", sysBal)
	}

	// Bob claims with the preimage.
	pw0 := binary.BigEndian.Uint64(secret[0:8])
	pw1 := binary.BigEndian.Uint64(secret[8:16])
	pw2 := binary.BigEndian.Uint64(secret[16:24])
	pw3 := binary.BigEndian.Uint64(secret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, pw0, pw1, pw2, pw3) // htlc_id=0
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ = exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if !receipts[0].Success {
		t.Fatalf("claim failed: %s", receipts[0].Err)
	}

	// Verify bob got the funds.
	bobBal := stateDB.GetBalance(bob)
	t.Logf("bob balance after claim: %d", bobBal)
	if bobBal < 1_020_000 { // started with 1M, got 50k, minus gas
		t.Fatalf("bob should have received funds, got %d", bobBal)
	}

	// Verify system address is empty.
	sysBal = stateDB.GetBalance(HTLCSystemAddress)
	if sysBal != 0 {
		t.Fatalf("system balance should be 0, got %d", sysBal)
	}

	// Verify HTLC status is claimed.
	status := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+8)
	if status != htlcStatusClaimed {
		t.Fatalf("expected status claimed (1), got %d", status)
	}

	// Verify preimage was persisted.
	storedPW0 := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+9)
	storedPW1 := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+10)
	storedPW2 := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+11)
	storedPW3 := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+12)
	if storedPW0 != pw0 || storedPW1 != pw1 || storedPW2 != pw2 || storedPW3 != pw3 {
		t.Fatal("persisted preimage does not match revealed preimage")
	}
}

func TestHTLCLockRefundFlow(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	_, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)

	secret := [32]byte{0xAA, 0xBB}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	// Lock at block 1, timelock at block 10.
	lockData := packCallData(HTLCFnLock, bobCallerID, 10, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 30000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)

	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	aliceAcctAfterLock := stateDB.GetAccount(alice)
	t.Logf("alice nonce after lock: %d, balance: %d", aliceAcctAfterLock.Nonce, aliceAcctAfterLock.Balance)

	// Try refund before timelock — should fail.
	refundData := packCallData(HTLCFnRefund, 0) // htlc_id=0
	refundTx := &types.Transaction{
		Version: 1, Nonce: 1, From: alice, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: refundData,
	}
	signTx(t, refundTx, aliceKey)

	_, receipts, _ := exec.ProcessBlock(5, types.ZeroHash, []*types.Transaction{refundTx})
	if receipts[0].Success {
		t.Fatal("refund should fail before timelock")
	}
	t.Logf("early refund correctly failed: %s", receipts[0].Err)

	// Check alice's nonce after the failed refund.
	aliceAcct := stateDB.GetAccount(alice)
	t.Logf("alice nonce after failed refund: %d", aliceAcct.Nonce)
	if aliceAcct.Nonce != 2 {
		t.Fatalf("expected nonce 2 after failed refund, got %d", aliceAcct.Nonce)
	}

	// Refund after timelock.
	refundTx2 := &types.Transaction{
		Version: 1, Nonce: 2, From: alice, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: refundData,
	}
	signTx(t, refundTx2, aliceKey)

	_, receipts, _ = exec.ProcessBlock(11, types.ZeroHash, []*types.Transaction{refundTx2})
	if !receipts[0].Success {
		t.Fatalf("refund after timelock failed: %s", receipts[0].Err)
	}

	// Verify system address is empty.
	sysBal := stateDB.GetBalance(HTLCSystemAddress)
	if sysBal != 0 {
		t.Fatalf("system balance should be 0, got %d", sysBal)
	}

	// Verify status is refunded.
	status := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+8)
	if status != htlcStatusRefunded {
		t.Fatalf("expected status refunded (2), got %d", status)
	}
}

func TestHTLCBadPreimage(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)
	stateDB.SetBalance(bob, 1_000_000)

	secret := [32]byte{0xDE, 0xAD}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 10000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)

	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// Bob tries to claim with wrong preimage.
	badSecret := [32]byte{0xFF, 0xFF}
	bw0 := binary.BigEndian.Uint64(badSecret[0:8])
	bw1 := binary.BigEndian.Uint64(badSecret[8:16])
	bw2 := binary.BigEndian.Uint64(badSecret[16:24])
	bw3 := binary.BigEndian.Uint64(badSecret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, bw0, bw1, bw2, bw3)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if receipts[0].Success {
		t.Fatal("claim with bad preimage should fail")
	}
	t.Logf("bad preimage correctly rejected: %s", receipts[0].Err)
}

func TestHTLCWrongRecipient(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	_, bob := testKey(t, 0xB)
	eveKey, eve := testKey(t, 0xC)

	stateDB.SetBalance(alice, 1_000_000)
	stateDB.SetBalance(eve, 1_000_000)

	secret := [32]byte{0x42}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 10000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)

	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// Eve tries to claim (she's not the recipient).
	pw0 := binary.BigEndian.Uint64(secret[0:8])
	pw1 := binary.BigEndian.Uint64(secret[8:16])
	pw2 := binary.BigEndian.Uint64(secret[16:24])
	pw3 := binary.BigEndian.Uint64(secret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, pw0, pw1, pw2, pw3)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: eve, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: claimData,
	}
	signTx(t, claimTx, eveKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if receipts[0].Success {
		t.Fatal("claim by wrong recipient should fail")
	}
	t.Logf("wrong recipient correctly rejected: %s", receipts[0].Err)
}

func TestHTLCQuery(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)
	stateDB.SetBalance(bob, 1_000_000)

	secret := [32]byte{0x99}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	lockData := packCallData(HTLCFnLock, bobCallerID, 50, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 25000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)

	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// Query HTLC 0.
	queryData := packCallData(HTLCFnQuery, 0)
	queryTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: queryData,
	}
	signTx(t, queryTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{queryTx})
	if !receipts[0].Success {
		t.Fatalf("query failed: %s", receipts[0].Err)
	}

	aliceCallerID := binary.BigEndian.Uint64(alice[12:20])
	senderID := readHTLCSlot(stateDB, 200)
	recipientID := readHTLCSlot(stateDB, 201)
	amount := readHTLCSlot(stateDB, 202)
	timelock := readHTLCSlot(stateDB, 203)
	status := readHTLCSlot(stateDB, 204)

	if senderID != aliceCallerID {
		t.Fatalf("expected sender %d, got %d", aliceCallerID, senderID)
	}
	if recipientID != bobCallerID {
		t.Fatalf("expected recipient %d, got %d", bobCallerID, recipientID)
	}
	if amount != 25000 {
		t.Fatalf("expected amount 25000, got %d", amount)
	}
	if timelock != 50 {
		t.Fatalf("expected timelock 50, got %d", timelock)
	}
	if status != htlcStatusPending {
		t.Fatalf("expected status pending (0), got %d", status)
	}
	t.Logf("query: sender=%d recipient=%d amount=%d timelock=%d status=%d", senderID, recipientID, amount, timelock, status)
}

// TestHTLCGaslessClaimZeroBalance verifies that a recipient with zero balance
// can claim an HTLC using the gasless claim path.
func TestHTLCGaslessClaimZeroBalance(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)
	// Bob has ZERO balance — cannot pay gas.

	secret := [32]byte{0x10, 0x20, 0x30}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	// Alice locks 50000 for Bob.
	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 50000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// Bob claims with GasPrice=0, GasLimit=0 (gasless path).
	pw0 := binary.BigEndian.Uint64(secret[0:8])
	pw1 := binary.BigEndian.Uint64(secret[8:16])
	pw2 := binary.BigEndian.Uint64(secret[16:24])
	pw3 := binary.BigEndian.Uint64(secret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, pw0, pw1, pw2, pw3)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 0, GasLimit: 0, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if !receipts[0].Success {
		t.Fatalf("gasless claim should succeed, got: %s", receipts[0].Err)
	}
	if receipts[0].GasUsed != 0 {
		t.Fatalf("gasless claim should use 0 gas, got %d", receipts[0].GasUsed)
	}

	// Bob should now have the funds.
	bobBal := stateDB.GetBalance(bob)
	if bobBal != 50000 {
		t.Fatalf("bob should have 50000, got %d", bobBal)
	}

	// Bob's nonce should be incremented.
	bobAcct := stateDB.GetAccount(bob)
	if bobAcct.Nonce != 1 {
		t.Fatalf("bob nonce should be 1, got %d", bobAcct.Nonce)
	}
}

// TestHTLCGaslessClaimRejectsValue verifies that a gasless claim with Value > 0 is rejected.
func TestHTLCGaslessClaimRejectsValue(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)
	stateDB.SetBalance(bob, 1_000_000)

	secret := [32]byte{0x50}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 10000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// Bob tries gasless claim with Value > 0.
	pw0 := binary.BigEndian.Uint64(secret[0:8])
	pw1 := binary.BigEndian.Uint64(secret[8:16])
	pw2 := binary.BigEndian.Uint64(secret[16:24])
	pw3 := binary.BigEndian.Uint64(secret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, pw0, pw1, pw2, pw3)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		Value: 1, GasPrice: 0, GasLimit: 0, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if receipts[0].Success {
		t.Fatal("gasless claim with Value > 0 should be rejected")
	}
	if receipts[0].Err != "gasless claim must have zero value" {
		t.Fatalf("unexpected error: %s", receipts[0].Err)
	}
}

// TestHTLCGaslessClaimWrongPreimage verifies that a failed gasless claim
// still increments nonce but doesn't transfer funds.
func TestHTLCGaslessClaimWrongPreimage(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)
	// Bob has zero balance.

	secret := [32]byte{0x77}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 10000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// Bob tries gasless claim with WRONG preimage.
	badSecret := [32]byte{0xFF}
	bw0 := binary.BigEndian.Uint64(badSecret[0:8])
	bw1 := binary.BigEndian.Uint64(badSecret[8:16])
	bw2 := binary.BigEndian.Uint64(badSecret[16:24])
	bw3 := binary.BigEndian.Uint64(badSecret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, bw0, bw1, bw2, bw3)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 0, GasLimit: 0, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if receipts[0].Success {
		t.Fatal("gasless claim with wrong preimage should fail")
	}
	if receipts[0].GasUsed != 0 {
		t.Fatalf("failed gasless claim should use 0 gas, got %d", receipts[0].GasUsed)
	}

	// Bob's nonce should still be incremented (replay prevention).
	bobAcct := stateDB.GetAccount(bob)
	if bobAcct.Nonce != 1 {
		t.Fatalf("bob nonce should be 1 after failed gasless claim, got %d", bobAcct.Nonce)
	}

	// Bob should still have zero balance.
	if stateDB.GetBalance(bob) != 0 {
		t.Fatal("bob should still have zero balance after failed claim")
	}

	// HTLC should still be pending.
	status := readHTLCSlot(stateDB, htlcSlotBase+0*htlcSlotStride+8)
	if status != htlcStatusPending {
		t.Fatalf("HTLC should still be pending, got status %d", status)
	}
}

// TestHTLCClaimNonexistent verifies that claiming a non-existent HTLC fails.
func TestHTLCClaimNonexistent(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	bobKey, bob := testKey(t, 0xB)
	stateDB.SetBalance(bob, 1_000_000)

	// Try to claim HTLC 0 when no HTLCs exist.
	claimData := packCallData(HTLCFnClaim, 0, 1, 2, 3, 4)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{claimTx})
	if receipts[0].Success {
		t.Fatal("claim of non-existent HTLC should fail")
	}
	if receipts[0].Err != "HTLC 0: does not exist" {
		t.Fatalf("unexpected error: %s", receipts[0].Err)
	}
}

// TestHTLCRefundNonexistent verifies that refunding a non-existent HTLC fails.
func TestHTLCRefundNonexistent(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	stateDB.SetBalance(alice, 1_000_000)

	refundData := packCallData(HTLCFnRefund, 99)
	refundTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: refundData,
	}
	signTx(t, refundTx, aliceKey)

	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{refundTx})
	if receipts[0].Success {
		t.Fatal("refund of non-existent HTLC should fail")
	}
	if receipts[0].Err != "HTLC 99: does not exist" {
		t.Fatalf("unexpected error: %s", receipts[0].Err)
	}
}

// TestHTLCDoubleClaimFails verifies that claiming an already-claimed HTLC fails.
func TestHTLCDoubleClaimFails(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := New(stateDB)

	aliceKey, alice := testKey(t, 0xA)
	bobKey, bob := testKey(t, 0xB)

	stateDB.SetBalance(alice, 1_000_000)

	secret := [32]byte{0xDD}
	hashlock := types.Sha256(secret[:])

	bobCallerID := binary.BigEndian.Uint64(bob[12:20])
	hw0 := binary.BigEndian.Uint64(hashlock[0:8])
	hw1 := binary.BigEndian.Uint64(hashlock[8:16])
	hw2 := binary.BigEndian.Uint64(hashlock[16:24])
	hw3 := binary.BigEndian.Uint64(hashlock[24:32])

	// Lock.
	lockData := packCallData(HTLCFnLock, bobCallerID, 100, hw0, hw1, hw2, hw3)
	lockTx := &types.Transaction{
		Version: 1, Nonce: 0, From: alice, To: HTLCSystemAddress,
		Value: 10000, GasPrice: 1, GasLimit: 100_000, Data: lockData,
	}
	signTx(t, lockTx, aliceKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{lockTx})

	// First claim (gasless).
	pw0 := binary.BigEndian.Uint64(secret[0:8])
	pw1 := binary.BigEndian.Uint64(secret[8:16])
	pw2 := binary.BigEndian.Uint64(secret[16:24])
	pw3 := binary.BigEndian.Uint64(secret[24:32])

	claimData := packCallData(HTLCFnClaim, 0, pw0, pw1, pw2, pw3)
	claimTx := &types.Transaction{
		Version: 1, Nonce: 0, From: bob, To: HTLCSystemAddress,
		GasPrice: 0, GasLimit: 0, Data: claimData,
	}
	signTx(t, claimTx, bobKey)

	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{claimTx})
	if !receipts[0].Success {
		t.Fatalf("first claim should succeed: %s", receipts[0].Err)
	}

	// Second claim (should fail — already claimed).
	claimTx2 := &types.Transaction{
		Version: 1, Nonce: 1, From: bob, To: HTLCSystemAddress,
		GasPrice: 0, GasLimit: 0, Data: claimData,
	}
	signTx(t, claimTx2, bobKey)

	_, receipts, _ = exec.ProcessBlock(3, types.ZeroHash, []*types.Transaction{claimTx2})
	if receipts[0].Success {
		t.Fatal("double claim should fail")
	}
	t.Logf("double claim correctly rejected: %s", receipts[0].Err)
}

// --- helpers ---

func packCallData(args ...uint64) []byte {
	data := make([]byte, len(args)*8)
	for i, arg := range args {
		binary.BigEndian.PutUint64(data[i*8:], arg)
	}
	return data
}

func readHTLCSlot(stateDB *state.StateDB, slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	val := stateDB.GetStorage(HTLCSystemAddress, key)
	return binary.BigEndian.Uint64(val[24:])
}
