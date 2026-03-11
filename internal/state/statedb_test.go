package state

import (
	"encoding/binary"
	"testing"

	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

func slotKey(slot uint64) types.Hash256 {
	var k types.Hash256
	binary.BigEndian.PutUint64(k[24:], slot)
	return k
}

func slotVal(v uint64) types.Hash256 {
	var h types.Hash256
	binary.BigEndian.PutUint64(h[24:], v)
	return h
}

func readSlotVal(h types.Hash256) uint64 {
	return binary.BigEndian.Uint64(h[24:])
}

// TestFinalizedStorageLazyLoad verifies that GetFinalizedStorage lazily loads
// storage tries that weren't cached when SnapshotFinalized was called.
// This simulates the restart scenario: the global trie snapshot is restored
// but storage tries are empty.
func TestFinalizedStorageLazyLoad(t *testing.T) {
	// Use a shared MemStore per address so data persists across trie instances.
	stores := make(map[types.Address]merkle.NodeStore)
	factory := func(addr types.Address) merkle.NodeStore {
		if s, ok := stores[addr]; ok {
			return s
		}
		s := merkle.NewMemStore()
		stores[addr] = s
		return s
	}

	globalStore := merkle.NewMemStore()
	sdb := NewStateDBWithFactory(globalStore, factory)

	contract := types.BytesToAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 42})
	user := types.BytesToAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 99})

	// Write storage to the contract.
	sdb.SetStorage(contract, slotKey(100), slotVal(1234))
	sdb.SetStorage(contract, slotKey(101), slotVal(5678))

	// Set a user balance.
	sdb.SetBalance(user, 50000)

	// Take finalized snapshot with storage tries loaded.
	sdb.SnapshotFinalized()

	// Verify finalized storage works normally.
	val, ok := sdb.GetFinalizedStorage(contract, slotKey(100))
	if !ok {
		t.Fatal("expected finalized snapshot to exist")
	}
	if readSlotVal(val) != 1234 {
		t.Fatalf("expected 1234, got %d", readSlotVal(val))
	}

	bal, ok := sdb.GetFinalizedBalance(user)
	if !ok || bal != 50000 {
		t.Fatalf("expected finalized balance 50000, got %d (ok=%v)", bal, ok)
	}

	// Now simulate a restart: flush to persist, then create a fresh stateDB
	// with empty storage tries, mimicking the startup path.
	root := sdb.Root()
	if err := sdb.FlushTries(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	sdb2 := NewStateDBWithFactory(globalStore, factory)
	sdb2.LoadTrieRoot(root)
	sdb2.ClearStorageTries()
	sdb2.SnapshotFinalized()

	// Verify the storage trie snapshot is empty (no addresses cached).
	if len(sdb2.finalizedStorageSnap) != 0 {
		t.Fatalf("expected empty storage snap after ClearStorageTries, got %d entries", len(sdb2.finalizedStorageSnap))
	}

	// GetFinalizedBalance should still work (global trie snapshot).
	bal2, ok := sdb2.GetFinalizedBalance(user)
	if !ok || bal2 != 50000 {
		t.Fatalf("expected finalized balance 50000 after restart, got %d (ok=%v)", bal2, ok)
	}

	// GetFinalizedStorage should lazily load the storage trie and return correct values.
	val2, ok := sdb2.GetFinalizedStorage(contract, slotKey(100))
	if !ok {
		t.Fatal("expected finalized snapshot to exist after restart")
	}
	if readSlotVal(val2) != 1234 {
		t.Fatalf("expected finalized storage 1234 after restart, got %d", readSlotVal(val2))
	}

	val3, ok := sdb2.GetFinalizedStorage(contract, slotKey(101))
	if !ok {
		t.Fatal("expected finalized snapshot to exist for slot 101")
	}
	if readSlotVal(val3) != 5678 {
		t.Fatalf("expected finalized storage 5678, got %d", readSlotVal(val3))
	}

	// Verify lazy-loaded trie is now cached.
	if len(sdb2.finalizedStorageSnap) != 1 {
		t.Fatalf("expected 1 cached storage snap after lazy load, got %d", len(sdb2.finalizedStorageSnap))
	}
}

// TestFinalizedStorageNoSnapshot verifies GetFinalizedStorage returns false
// when no finalized snapshot exists.
func TestFinalizedStorageNoSnapshot(t *testing.T) {
	sdb := NewStateDB(merkle.NewMemStore())
	contract := types.BytesToAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})

	sdb.SetStorage(contract, slotKey(0), slotVal(42))

	_, ok := sdb.GetFinalizedStorage(contract, slotKey(0))
	if ok {
		t.Fatal("expected ok=false when no finalized snapshot")
	}
}

// TestFinalizedStorageNonexistentAccount verifies GetFinalizedStorage returns
// zero for an account that didn't exist at the finalized point.
func TestFinalizedStorageNonexistentAccount(t *testing.T) {
	sdb := NewStateDB(merkle.NewMemStore())
	sdb.SetBalance(types.BytesToAddress([]byte{1}), 100)
	sdb.SnapshotFinalized()

	ghost := types.BytesToAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 77})
	val, ok := sdb.GetFinalizedStorage(ghost, slotKey(0))
	if !ok {
		t.Fatal("expected ok=true with finalized snapshot")
	}
	if !val.IsZero() {
		t.Fatal("expected zero for nonexistent account")
	}
}

// TestFinalizedStoragePostSnapshotWritesInvisible verifies that writes after
// SnapshotFinalized are not visible in the finalized view.
func TestFinalizedStoragePostSnapshotWritesInvisible(t *testing.T) {
	stores := make(map[types.Address]merkle.NodeStore)
	factory := func(addr types.Address) merkle.NodeStore {
		if s, ok := stores[addr]; ok {
			return s
		}
		s := merkle.NewMemStore()
		stores[addr] = s
		return s
	}

	sdb := NewStateDBWithFactory(merkle.NewMemStore(), factory)
	contract := types.BytesToAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5})

	sdb.SetStorage(contract, slotKey(0), slotVal(100))
	sdb.SnapshotFinalized()

	// Write new value after snapshot.
	sdb.SetStorage(contract, slotKey(0), slotVal(200))
	sdb.SetStorage(contract, slotKey(1), slotVal(300))

	// Current state sees new values.
	if readSlotVal(sdb.GetStorage(contract, slotKey(0))) != 200 {
		t.Fatal("current state should see 200")
	}

	// Finalized state sees old values.
	val, ok := sdb.GetFinalizedStorage(contract, slotKey(0))
	if !ok || readSlotVal(val) != 100 {
		t.Fatalf("finalized should see 100, got %d (ok=%v)", readSlotVal(val), ok)
	}

	// Slot 1 didn't exist at finalized point.
	val1, ok := sdb.GetFinalizedStorage(contract, slotKey(1))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !val1.IsZero() {
		t.Fatalf("finalized slot 1 should be zero, got %d", readSlotVal(val1))
	}
}
