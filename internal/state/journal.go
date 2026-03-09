package state

import "github.com/djkazic/tylium/pkg/types"

// Journal tracks state mutations for revert support within a block.
// When a transaction fails, its changes can be rolled back without
// affecting prior successful transactions.

// JournalEntry records a single state mutation.
type JournalEntry struct {
	Type    entryType
	Address types.Address
	Key     types.Hash256 // for storage entries
	OldData []byte        // serialized account or storage value
}

type entryType int

const (
	entryAccount entryType = iota
	entryStorage
)

// Journal manages revert snapshots.
type Journal struct {
	entries   []JournalEntry
	snapshots []int // indices into entries
}

func NewJournal() *Journal {
	return &Journal{}
}

// Snapshot records the current journal position.
func (j *Journal) Snapshot() int {
	id := len(j.snapshots)
	j.snapshots = append(j.snapshots, len(j.entries))
	return id
}

// RevertTo replays journal entries in reverse to undo changes since the snapshot.
func (j *Journal) RevertTo(id int, state *StateDB) {
	if id >= len(j.snapshots) {
		return
	}
	target := j.snapshots[id]
	for i := len(j.entries) - 1; i >= target; i-- {
		entry := j.entries[i]
		switch entry.Type {
		case entryAccount:
			if entry.OldData == nil {
				// Account didn't exist before; remove it.
				key := types.Sha256(entry.Address[:])
				state.globalTrie.Delete(key)
			} else {
				key := types.Sha256(entry.Address[:])
				state.globalTrie.Set(key, entry.OldData)
			}
		case entryStorage:
			trie := state.getStorageTrie(entry.Address)
			if entry.OldData == nil {
				trie.Delete(entry.Key)
			} else {
				trie.Set(entry.Key, entry.OldData)
			}
		}
	}
	j.entries = j.entries[:target]
	j.snapshots = j.snapshots[:id]
}

// RecordAccount logs the current account state before modification.
func (j *Journal) RecordAccount(addr types.Address, oldData []byte) {
	j.entries = append(j.entries, JournalEntry{
		Type:    entryAccount,
		Address: addr,
		OldData: oldData,
	})
}

// RecordStorage logs the current storage value before modification.
func (j *Journal) RecordStorage(addr types.Address, key types.Hash256, oldData []byte) {
	j.entries = append(j.entries, JournalEntry{
		Type:    entryStorage,
		Address: addr,
		Key:     key,
		OldData: oldData,
	})
}

// Clear removes all journal entries and snapshots.
func (j *Journal) Clear() {
	j.entries = j.entries[:0]
	j.snapshots = j.snapshots[:0]
}
