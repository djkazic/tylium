package executor

import (
	"encoding/binary"
	"fmt"

	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/types"
)

// HTLCSystemAddress is the well-known address for the HTLC precompile.
// Funds locked in HTLCs are held at this address's balance.
var HTLCSystemAddress = types.BytesToAddress([]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1,
})

// HTLC function selectors (via call data slot 100).
const (
	HTLCFnLock   = 1
	HTLCFnClaim  = 2
	HTLCFnRefund = 3
	HTLCFnQuery  = 4
)

// HTLC storage layout at HTLCSystemAddress:
//
//	Slot 0: next HTLC ID counter
//
// Per HTLC (base = 10000 + id * 14):
//
//	+0: sender caller_id
//	+1: recipient caller_id
//	+2: amount
//	+3: hashlock word 0 (bytes 0-7)
//	+4: hashlock word 1 (bytes 8-15)
//	+5: hashlock word 2 (bytes 16-23)
//	+6: hashlock word 3 (bytes 24-31)
//	+7: timelock block height
//	+8: status (0=pending, 1=claimed, 2=refunded)
//	+9: preimage word 0 (set on claim)
//	+10: preimage word 1
//	+11: preimage word 2
//	+12: preimage word 3
const (
	htlcSlotCounter = 0
	htlcSlotBase    = 10000
	htlcSlotStride  = 14

	htlcStatusPending  = 0
	htlcStatusClaimed  = 1
	htlcStatusRefunded = 2
)

// htlcHandler processes HTLC operations as a native precompile.
type htlcHandler struct {
	stateDB      *state.StateDB
	currentHeight uint64
}

// processHTLC routes an HTLC call based on the function selector.
// The caller has already transferred tx.Value to HTLCSystemAddress.
func (h *htlcHandler) processHTLC(tx *types.Transaction) (bool, string) {
	selector := h.readSlot(100)
	callerID := h.readSlot(103)

	switch selector {
	case HTLCFnLock:
		return h.lock(tx, callerID)
	case HTLCFnClaim:
		return h.claim(tx, callerID)
	case HTLCFnRefund:
		return h.refund(tx, callerID)
	case HTLCFnQuery:
		return h.query()
	default:
		return false, fmt.Sprintf("unknown HTLC function: %d", selector)
	}
}

// lock creates a new HTLC.
// Call data: selector=1, recipient_id(101), timelock(102), [103=caller],
//
//	hashlock_w0(104), hashlock_w1(105), hashlock_w2(106), hashlock_w3(107)
//
// tx.Value = amount to lock (already transferred to system address by executeTx).
func (h *htlcHandler) lock(tx *types.Transaction, senderID uint64) (bool, string) {
	recipientID := h.readSlot(101)
	timelock := h.readSlot(102)
	hw0 := h.readSlot(104)
	hw1 := h.readSlot(105)
	hw2 := h.readSlot(106)
	hw3 := h.readSlot(107)

	if tx.Value == 0 {
		return false, "HTLC lock: amount must be > 0"
	}
	if timelock <= h.currentHeight {
		return false, "HTLC lock: timelock must be in the future"
	}

	// Allocate new HTLC ID.
	htlcID := h.readSlot(htlcSlotCounter)
	h.writeSlot(htlcSlotCounter, htlcID+1)

	// Store HTLC.
	base := htlcSlotBase + htlcID*htlcSlotStride
	h.writeSlot(base+0, senderID)
	h.writeSlot(base+1, recipientID)
	h.writeSlot(base+2, tx.Value)
	h.writeSlot(base+3, hw0)
	h.writeSlot(base+4, hw1)
	h.writeSlot(base+5, hw2)
	h.writeSlot(base+6, hw3)
	h.writeSlot(base+7, timelock)
	h.writeSlot(base+8, htlcStatusPending)

	// Write HTLC ID to return slot.
	h.writeSlot(200, htlcID)

	return true, ""
}

// claim settles an HTLC by revealing the preimage.
// Call data: selector=2, htlc_id(101), preimage_w0(102), [103=caller],
//
//	preimage_w1(104), preimage_w2(105), preimage_w3(106)
//
// The caller must be the designated recipient.
func (h *htlcHandler) claim(tx *types.Transaction, callerID uint64) (bool, string) {
	htlcID := h.readSlot(101)
	if htlcID >= h.readSlot(htlcSlotCounter) {
		return false, fmt.Sprintf("HTLC %d: does not exist", htlcID)
	}
	pw0 := h.readSlot(102)
	pw1 := h.readSlot(104)
	pw2 := h.readSlot(105)
	pw3 := h.readSlot(106)

	base := htlcSlotBase + htlcID*htlcSlotStride

	// Verify HTLC exists and is pending.
	status := h.readSlot(base + 8)
	if status != htlcStatusPending {
		return false, fmt.Sprintf("HTLC %d: not pending (status=%d)", htlcID, status)
	}

	// Verify caller is the recipient.
	recipientID := h.readSlot(base + 1)
	if callerID != recipientID {
		return false, fmt.Sprintf("HTLC %d: caller %d is not recipient %d", htlcID, callerID, recipientID)
	}

	// Reconstruct preimage and hashlock, verify SHA256(preimage) == hashlock.
	var preimage [32]byte
	binary.BigEndian.PutUint64(preimage[0:8], pw0)
	binary.BigEndian.PutUint64(preimage[8:16], pw1)
	binary.BigEndian.PutUint64(preimage[16:24], pw2)
	binary.BigEndian.PutUint64(preimage[24:32], pw3)

	hash := types.Sha256(preimage[:])

	hw0 := h.readSlot(base + 3)
	hw1 := h.readSlot(base + 4)
	hw2 := h.readSlot(base + 5)
	hw3 := h.readSlot(base + 6)

	var hashlock [32]byte
	binary.BigEndian.PutUint64(hashlock[0:8], hw0)
	binary.BigEndian.PutUint64(hashlock[8:16], hw1)
	binary.BigEndian.PutUint64(hashlock[16:24], hw2)
	binary.BigEndian.PutUint64(hashlock[24:32], hw3)

	if hash != types.Hash256(hashlock) {
		return false, fmt.Sprintf("HTLC %d: preimage does not match hashlock", htlcID)
	}

	// Transfer funds from system address to recipient.
	amount := h.readSlot(base + 2)
	sysAcct := h.stateDB.GetOrCreateAccount(HTLCSystemAddress)
	if sysAcct.Balance < amount {
		return false, fmt.Sprintf("HTLC %d: system balance insufficient", htlcID)
	}
	sysAcct.Balance -= amount
	h.stateDB.SetAccount(HTLCSystemAddress, sysAcct)

	recipientAcct := h.stateDB.GetOrCreateAccount(tx.From) // caller IS the recipient
	recipientAcct.Balance += amount
	h.stateDB.SetAccount(tx.From, recipientAcct)

	// Mark as claimed and persist the revealed preimage.
	h.writeSlot(base+8, htlcStatusClaimed)
	h.writeSlot(base+9, pw0)
	h.writeSlot(base+10, pw1)
	h.writeSlot(base+11, pw2)
	h.writeSlot(base+12, pw3)

	return true, ""
}

// refund returns HTLC funds to the sender after timelock expires.
// Call data: selector=3, htlc_id(101)
func (h *htlcHandler) refund(tx *types.Transaction, callerID uint64) (bool, string) {
	htlcID := h.readSlot(101)
	if htlcID >= h.readSlot(htlcSlotCounter) {
		return false, fmt.Sprintf("HTLC %d: does not exist", htlcID)
	}
	base := htlcSlotBase + htlcID*htlcSlotStride

	// Verify HTLC exists and is pending.
	status := h.readSlot(base + 8)
	if status != htlcStatusPending {
		return false, fmt.Sprintf("HTLC %d: not pending (status=%d)", htlcID, status)
	}

	// Verify caller is the sender.
	senderID := h.readSlot(base + 0)
	if callerID != senderID {
		return false, fmt.Sprintf("HTLC %d: caller %d is not sender %d", htlcID, callerID, senderID)
	}

	// Verify timelock has expired.
	timelock := h.readSlot(base + 7)
	if h.currentHeight < timelock {
		return false, fmt.Sprintf("HTLC %d: timelock not expired (current=%d, lock=%d)", htlcID, h.currentHeight, timelock)
	}

	// Transfer funds from system address back to sender.
	amount := h.readSlot(base + 2)
	sysAcct := h.stateDB.GetOrCreateAccount(HTLCSystemAddress)
	if sysAcct.Balance < amount {
		return false, fmt.Sprintf("HTLC %d: system balance insufficient", htlcID)
	}
	sysAcct.Balance -= amount
	h.stateDB.SetAccount(HTLCSystemAddress, sysAcct)

	senderAcct := h.stateDB.GetOrCreateAccount(tx.From) // caller IS the sender
	senderAcct.Balance += amount
	h.stateDB.SetAccount(tx.From, senderAcct)

	// Mark as refunded.
	h.writeSlot(base+8, htlcStatusRefunded)

	return true, ""
}

// query writes HTLC info to return slots.
// Call data: selector=4, htlc_id(101)
// Returns: 200=sender_id, 201=recipient_id, 202=amount, 203=timelock, 204=status,
//
//	205-208=preimage words (only meaningful when status=claimed)
func (h *htlcHandler) query() (bool, string) {
	htlcID := h.readSlot(101)
	if htlcID >= h.readSlot(htlcSlotCounter) {
		return false, fmt.Sprintf("HTLC %d: does not exist", htlcID)
	}
	base := htlcSlotBase + htlcID*htlcSlotStride

	h.writeSlot(200, h.readSlot(base+0))  // sender_id
	h.writeSlot(201, h.readSlot(base+1))  // recipient_id
	h.writeSlot(202, h.readSlot(base+2))  // amount
	h.writeSlot(203, h.readSlot(base+7))  // timelock
	h.writeSlot(204, h.readSlot(base+8))  // status
	h.writeSlot(205, h.readSlot(base+9))  // preimage w0
	h.writeSlot(206, h.readSlot(base+10)) // preimage w1
	h.writeSlot(207, h.readSlot(base+11)) // preimage w2
	h.writeSlot(208, h.readSlot(base+12)) // preimage w3

	return true, ""
}

// readSlot reads a uint64 from a storage slot at HTLCSystemAddress.
func (h *htlcHandler) readSlot(slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	val := h.stateDB.GetStorage(HTLCSystemAddress, key)
	return binary.BigEndian.Uint64(val[24:])
}

// writeSlot writes a uint64 to a storage slot at HTLCSystemAddress.
func (h *htlcHandler) writeSlot(slot, val uint64) {
	var key, v types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	binary.BigEndian.PutUint64(v[24:], val)
	h.stateDB.SetStorage(HTLCSystemAddress, key, v)
}
