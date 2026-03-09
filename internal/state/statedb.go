package state

import (
	"github.com/djkazic/tylium/internal/vm"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// StateDB is the global state database backed by a sparse Merkle tree.
// It implements vm.StateAccessor.
type StateDB struct {
	globalTrie   *merkle.Tree
	storageTries map[types.Address]*merkle.Tree
	storeFactory func(addr types.Address) merkle.NodeStore
	codes        map[types.Hash256][]byte // codeHash -> bytecode
	journal      *Journal

	// Finalized snapshots — capture tries at the last finalized height.
	finalizedSnap        *merkle.Snapshot            // global trie
	finalizedStorageSnap map[types.Address]*merkle.Snapshot // per-contract storage
}

// Ensure StateDB implements StateAccessor.
var _ vm.StateAccessor = (*StateDB)(nil)

// NewStateDB creates a new state database with an in-memory store factory.
func NewStateDB(store merkle.NodeStore) *StateDB {
	return &StateDB{
		globalTrie:   merkle.NewTree(store),
		storageTries: make(map[types.Address]*merkle.Tree),
		storeFactory: func(_ types.Address) merkle.NodeStore { return merkle.NewMemStore() },
		codes:        make(map[types.Hash256][]byte),
		journal:      NewJournal(),
	}
}

// NewStateDBWithFactory creates a state database with a custom storage trie factory.
func NewStateDBWithFactory(store merkle.NodeStore, factory func(addr types.Address) merkle.NodeStore) *StateDB {
	return &StateDB{
		globalTrie:   merkle.NewTree(store),
		storageTries: make(map[types.Address]*merkle.Tree),
		storeFactory: factory,
		codes:        make(map[types.Hash256][]byte),
		journal:      NewJournal(),
	}
}

// Root returns the global state root hash.
func (s *StateDB) Root() types.Hash256 {
	return s.globalTrie.Root()
}

// GetAccount retrieves an account, or nil if it doesn't exist.
func (s *StateDB) GetAccount(addr types.Address) *types.Account {
	key := types.Sha256(addr[:])
	data, _ := s.globalTrie.Get(key)
	if data == nil {
		return nil
	}
	acct, err := encoding.DecodeAccount(data)
	if err != nil {
		return nil
	}
	return acct
}

// SetAccount writes an account to the global trie.
func (s *StateDB) SetAccount(addr types.Address, acct *types.Account) {
	key := types.Sha256(addr[:])
	// Record old state for rollback.
	oldData, _ := s.globalTrie.Get(key)
	s.journal.RecordAccount(addr, oldData)
	data := encoding.EncodeAccount(acct)
	s.globalTrie.Set(key, data)
}

// GetOrCreateAccount retrieves an account, creating it if it doesn't exist.
func (s *StateDB) GetOrCreateAccount(addr types.Address) *types.Account {
	acct := s.GetAccount(addr)
	if acct == nil {
		acct = &types.Account{}
	}
	return acct
}

// GetBalance returns an account's balance.
func (s *StateDB) GetBalance(addr types.Address) uint64 {
	acct := s.GetAccount(addr)
	if acct == nil {
		return 0
	}
	return acct.Balance
}

// SetBalance sets an account's balance.
func (s *StateDB) SetBalance(addr types.Address, val uint64) {
	acct := s.GetOrCreateAccount(addr)
	acct.Balance = val
	s.SetAccount(addr, acct)
}

// GetStorage reads a value from an account's storage trie.
func (s *StateDB) GetStorage(addr types.Address, key types.Hash256) types.Hash256 {
	trie := s.getStorageTrie(addr)
	data, _ := trie.Get(key)
	if data == nil {
		return types.ZeroHash
	}
	return types.BytesToHash256(data)
}

// SetStorage writes a value to an account's storage trie.
func (s *StateDB) SetStorage(addr types.Address, key types.Hash256, val types.Hash256) {
	trie := s.getStorageTrie(addr)
	// Record old storage value for rollback.
	oldData, _ := trie.Get(key)
	s.journal.RecordStorage(addr, key, oldData)
	if val.IsZero() {
		trie.Delete(key)
	} else {
		trie.Set(key, val[:])
	}
	// Update account's storage root.
	acct := s.GetOrCreateAccount(addr)
	acct.StorageRoot = trie.Root()
	s.SetAccount(addr, acct)
}

// GetCode retrieves contract bytecode by its hash.
func (s *StateDB) GetCode(codeHash types.Hash256) []byte {
	return s.codes[codeHash]
}

// SetCode stores contract bytecode and updates the account's code hash.
func (s *StateDB) SetCode(addr types.Address, code []byte) {
	hash := types.Sha256(code)
	s.codes[hash] = code
	acct := s.GetOrCreateAccount(addr)
	acct.CodeHash = hash
	s.SetAccount(addr, acct)
}

// LoadCode loads code into the in-memory cache (used when restoring from persistent storage).
func (s *StateDB) LoadCode(hash types.Hash256, code []byte) {
	s.codes[hash] = code
}

// AllCodes returns the full in-memory code cache (codeHash -> bytecode).
func (s *StateDB) AllCodes() map[types.Hash256][]byte {
	return s.codes
}

// Snapshot returns a snapshot ID for later rollback.
func (s *StateDB) Snapshot() int {
	return s.journal.Snapshot()
}

// RevertToSnapshot rolls back state changes to a snapshot.
func (s *StateDB) RevertToSnapshot(id int) {
	s.journal.RevertTo(id, s)
}

// Commit flushes all pending state and returns the state root.
func (s *StateDB) Commit() types.Hash256 {
	s.journal.Clear()
	return s.Root()
}

// LoadTrieRoot sets the global trie root from a persisted hash. Storage tries
// are loaded lazily — getStorageTrie reads each account's StorageRoot.
func (s *StateDB) LoadTrieRoot(root types.Hash256) {
	s.globalTrie.LoadRoot(root)
}

// ClearStorageTries discards all cached storage tries, forcing them to be
// reloaded lazily from their persisted roots. Used after state rollback.
func (s *StateDB) ClearStorageTries() {
	s.storageTries = make(map[types.Address]*merkle.Tree)
	s.journal = NewJournal()
}

// FlushTries persists all in-memory trie nodes to their backing stores.
func (s *StateDB) FlushTries() error {
	if err := s.globalTrie.Flush(); err != nil {
		return err
	}
	for _, trie := range s.storageTries {
		if err := trie.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// SnapshotFinalized captures the current state as the finalized point.
func (s *StateDB) SnapshotFinalized() {
	s.finalizedSnap = s.globalTrie.TakeSnapshot()
	s.finalizedStorageSnap = make(map[types.Address]*merkle.Snapshot)
	for addr, trie := range s.storageTries {
		s.finalizedStorageSnap[addr] = trie.TakeSnapshot()
	}
}

// HasFinalizedSnapshot returns true if a finalized snapshot has been taken.
func (s *StateDB) HasFinalizedSnapshot() bool {
	return s.finalizedSnap != nil
}

// GetFinalizedBalance returns an account's balance at the last finalized snapshot.
// Returns (balance, true) if a finalized snapshot exists, (0, false) otherwise.
func (s *StateDB) GetFinalizedBalance(addr types.Address) (uint64, bool) {
	if s.finalizedSnap == nil {
		return 0, false
	}
	key := types.Sha256(addr[:])
	data := s.finalizedSnap.Get(key)
	if data == nil {
		return 0, true
	}
	acct, err := encoding.DecodeAccount(data)
	if err != nil {
		return 0, true
	}
	return acct.Balance, true
}

// GetFinalizedStorage returns a storage value at the last finalized snapshot.
// Returns (value, true) if a finalized snapshot exists, (zero, false) otherwise.
func (s *StateDB) GetFinalizedStorage(addr types.Address, key types.Hash256) (types.Hash256, bool) {
	if s.finalizedStorageSnap == nil {
		return types.ZeroHash, false
	}
	snap, ok := s.finalizedStorageSnap[addr]
	if !ok {
		return types.ZeroHash, true // contract didn't exist at finalized point
	}
	data := snap.Get(key)
	if data == nil {
		return types.ZeroHash, true
	}
	return types.BytesToHash256(data), true
}

func (s *StateDB) getStorageTrie(addr types.Address) *merkle.Tree {
	if trie, ok := s.storageTries[addr]; ok {
		return trie
	}
	trie := merkle.NewTree(s.storeFactory(addr))
	// Load persisted storage root if the account exists.
	acct := s.GetAccount(addr)
	if acct != nil && !acct.StorageRoot.IsZero() {
		trie.LoadRoot(acct.StorageRoot)
	}
	s.storageTries[addr] = trie
	return trie
}
