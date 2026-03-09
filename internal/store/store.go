package store

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Prefixes for namespace isolation within the single LevelDB.
var (
	prefixState    = []byte("s/") // global state trie nodes
	prefixStorage  = []byte("t/") // per-account storage trie nodes
	prefixCode     = []byte("c/") // contract bytecode
	prefixMeta     = []byte("m/") // metadata (height, state root, etc.)
	prefixBlock    = []byte("b/") // block data
	prefixReceipt  = []byte("r/") // receipt data
	prefixContract = []byte("k/") // contract callerID → address mapping
	prefixTxReceipt = []byte("x/") // txid → receipt JSON
	prefixTxIndex   = []byte("i/") // sequential index → txid (for history)
	prefixL1Hash    = []byte("l/") // L1 height → L1 block hash (for reorg detection)
)

// Meta keys for tx index counter.
var metaTxCount = []byte("txcount")

// Meta keys.
var (
	metaHeight        = []byte("height")
	metaStateRoot     = []byte("stateroot")
	metaFinalHeight   = []byte("finalheight")
	metaFinalRoot     = []byte("finalroot")
)

// Store is the persistent data layer backed by LevelDB.
type Store struct {
	db *leveldb.DB
}

// Open creates or opens a persistent store at the given directory.
func Open(dataDir string) (*Store, error) {
	dbPath := filepath.Join(dataDir, "tylium.db")
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return &Store{db: db}, nil
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// StateStore returns a NodeStore for the global state trie.
func (s *Store) StateStore() merkle.NodeStore {
	return merkle.NewLevelDBStoreFromDB(s.db, prefixState)
}

// StorageStore returns a NodeStore for a per-account storage trie.
func (s *Store) StorageStore(addr types.Address) merkle.NodeStore {
	prefix := make([]byte, 0, len(prefixStorage)+20)
	prefix = append(prefix, prefixStorage...)
	prefix = append(prefix, addr[:]...)
	return merkle.NewLevelDBStoreFromDB(s.db, prefix)
}

// GetCode retrieves contract bytecode by hash.
func (s *Store) GetCode(hash types.Hash256) ([]byte, error) {
	key := make([]byte, len(prefixCode)+32)
	copy(key, prefixCode)
	copy(key[len(prefixCode):], hash[:])

	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	return val, err
}

// PutCode stores contract bytecode.
func (s *Store) PutCode(hash types.Hash256, code []byte) error {
	key := make([]byte, len(prefixCode)+32)
	copy(key, prefixCode)
	copy(key[len(prefixCode):], hash[:])
	return s.db.Put(key, code, nil)
}

// ListCodes iterates all stored contract bytecodes and returns them as a map
// of codeHash -> bytecode. This is used on startup to restore codes into the
// in-memory StateDB so that RPC queries work without a full resync.
func (s *Store) ListCodes() (map[types.Hash256][]byte, error) {
	codes := make(map[types.Hash256][]byte)
	iter := s.db.NewIterator(util.BytesPrefix(prefixCode), nil)
	defer iter.Release()
	for iter.Next() {
		key := iter.Key()
		hash := types.BytesToHash256(key[len(prefixCode):])
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())
		codes[hash] = val
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("list codes: %w", err)
	}
	return codes, nil
}

// GetHeight retrieves the last processed L1 block height.
func (s *Store) GetHeight() (uint64, error) {
	key := append(prefixMeta, metaHeight...)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(val) != 8 {
		return 0, fmt.Errorf("invalid height data")
	}
	return binary.BigEndian.Uint64(val), nil
}

// PutHeight stores the last processed L1 block height.
func (s *Store) PutHeight(height uint64) error {
	key := append(prefixMeta, metaHeight...)
	val := make([]byte, 8)
	binary.BigEndian.PutUint64(val, height)
	return s.db.Put(key, val, nil)
}

// GetStateRoot retrieves the last committed state root.
func (s *Store) GetStateRoot() (types.Hash256, error) {
	key := append(prefixMeta, metaStateRoot...)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return types.ZeroHash, nil
	}
	if err != nil {
		return types.ZeroHash, err
	}
	return types.BytesToHash256(val), nil
}

// PutStateRoot stores the last committed state root.
func (s *Store) PutStateRoot(root types.Hash256) error {
	key := append(prefixMeta, metaStateRoot...)
	return s.db.Put(key, root[:], nil)
}

// PutBlock stores serialized block data keyed by height.
func (s *Store) PutBlock(height uint64, data []byte) error {
	key := make([]byte, len(prefixBlock)+8)
	copy(key, prefixBlock)
	binary.BigEndian.PutUint64(key[len(prefixBlock):], height)
	return s.db.Put(key, data, nil)
}

// GetBlock retrieves serialized block data by height.
func (s *Store) GetBlock(height uint64) ([]byte, error) {
	key := make([]byte, len(prefixBlock)+8)
	copy(key, prefixBlock)
	binary.BigEndian.PutUint64(key[len(prefixBlock):], height)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	return val, err
}

// PutContract stores a contract callerID → address mapping.
func (s *Store) PutContract(callerID uint64, addr types.Address) error {
	key := make([]byte, len(prefixContract)+8)
	copy(key, prefixContract)
	binary.BigEndian.PutUint64(key[len(prefixContract):], callerID)
	return s.db.Put(key, addr[:], nil)
}

// ListContracts returns all stored callerID → address mappings.
func (s *Store) ListContracts() (map[uint64]types.Address, error) {
	contracts := make(map[uint64]types.Address)
	iter := s.db.NewIterator(util.BytesPrefix(prefixContract), nil)
	defer iter.Release()
	for iter.Next() {
		key := iter.Key()
		callerID := binary.BigEndian.Uint64(key[len(prefixContract):])
		addr := types.BytesToAddress(iter.Value())
		contracts[callerID] = addr
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("list contracts: %w", err)
	}
	return contracts, nil
}

// PutReceipts stores serialized receipt data keyed by block height.
func (s *Store) PutReceipts(height uint64, data []byte) error {
	key := make([]byte, len(prefixReceipt)+8)
	copy(key, prefixReceipt)
	binary.BigEndian.PutUint64(key[len(prefixReceipt):], height)
	return s.db.Put(key, data, nil)
}

// GetFinalizedHeight retrieves the last finalized (attested) block height.
func (s *Store) GetFinalizedHeight() (uint64, error) {
	key := append(prefixMeta, metaFinalHeight...)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(val) != 8 {
		return 0, fmt.Errorf("invalid finalized height data")
	}
	return binary.BigEndian.Uint64(val), nil
}

// PutFinalizedHeight stores the last finalized block height.
func (s *Store) PutFinalizedHeight(height uint64) error {
	key := append(prefixMeta, metaFinalHeight...)
	val := make([]byte, 8)
	binary.BigEndian.PutUint64(val, height)
	return s.db.Put(key, val, nil)
}

// GetFinalizedRoot retrieves the state root of the last finalized block.
func (s *Store) GetFinalizedRoot() (types.Hash256, error) {
	key := append(prefixMeta, metaFinalRoot...)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return types.ZeroHash, nil
	}
	if err != nil {
		return types.ZeroHash, err
	}
	return types.BytesToHash256(val), nil
}

// PutFinalizedRoot stores the state root of the last finalized block.
func (s *Store) PutFinalizedRoot(root types.Hash256) error {
	key := append(prefixMeta, metaFinalRoot...)
	return s.db.Put(key, root[:], nil)
}

// GetReceipts retrieves serialized receipt data by block height.
func (s *Store) GetReceipts(height uint64) ([]byte, error) {
	key := make([]byte, len(prefixReceipt)+8)
	copy(key, prefixReceipt)
	binary.BigEndian.PutUint64(key[len(prefixReceipt):], height)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	return val, err
}

// PutTxReceipt stores a receipt JSON blob keyed by transaction ID.
func (s *Store) PutTxReceipt(txid types.Hash256, data []byte) error {
	key := make([]byte, len(prefixTxReceipt)+32)
	copy(key, prefixTxReceipt)
	copy(key[len(prefixTxReceipt):], txid[:])
	return s.db.Put(key, data, nil)
}

// GetTxReceipt retrieves a receipt JSON blob by transaction ID.
func (s *Store) GetTxReceipt(txid types.Hash256) ([]byte, error) {
	key := make([]byte, len(prefixTxReceipt)+32)
	copy(key, prefixTxReceipt)
	copy(key[len(prefixTxReceipt):], txid[:])
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	return val, err
}

// PutTxIndex appends a txid to the sequential transaction index.
func (s *Store) PutTxIndex(seq uint64, txid types.Hash256) error {
	key := make([]byte, len(prefixTxIndex)+8)
	copy(key, prefixTxIndex)
	binary.BigEndian.PutUint64(key[len(prefixTxIndex):], seq)
	return s.db.Put(key, txid[:], nil)
}

// GetTxCount returns the current transaction count (next sequential index).
func (s *Store) GetTxCount() (uint64, error) {
	key := append(prefixMeta, metaTxCount...)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(val) != 8 {
		return 0, fmt.Errorf("invalid txcount data")
	}
	return binary.BigEndian.Uint64(val), nil
}

// PutTxCount stores the current transaction count.
func (s *Store) PutTxCount(count uint64) error {
	key := append(prefixMeta, metaTxCount...)
	val := make([]byte, 8)
	binary.BigEndian.PutUint64(val, count)
	return s.db.Put(key, val, nil)
}

// PutL1Hash stores the L1 block hash for a given height (for reorg detection).
func (s *Store) PutL1Hash(height uint64, hash types.Hash256) error {
	key := make([]byte, len(prefixL1Hash)+8)
	copy(key, prefixL1Hash)
	binary.BigEndian.PutUint64(key[len(prefixL1Hash):], height)
	return s.db.Put(key, hash[:], nil)
}

// GetL1Hash retrieves the stored L1 block hash for a given height.
func (s *Store) GetL1Hash(height uint64) (types.Hash256, error) {
	key := make([]byte, len(prefixL1Hash)+8)
	copy(key, prefixL1Hash)
	binary.BigEndian.PutUint64(key[len(prefixL1Hash):], height)
	val, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return types.ZeroHash, nil
	}
	if err != nil {
		return types.ZeroHash, err
	}
	return types.BytesToHash256(val), nil
}

// DeleteL1HashesAbove removes all stored L1 block hashes above the given height.
func (s *Store) DeleteL1HashesAbove(height uint64) error {
	// Scan from height+1 up to whatever exists.
	start := make([]byte, len(prefixL1Hash)+8)
	copy(start, prefixL1Hash)
	binary.BigEndian.PutUint64(start[len(prefixL1Hash):], height+1)

	iter := s.db.NewIterator(&util.Range{Start: start, Limit: util.BytesPrefix(prefixL1Hash).Limit}, nil)
	defer iter.Release()

	batch := new(leveldb.Batch)
	for iter.Next() {
		batch.Delete(iter.Key())
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if batch.Len() > 0 {
		return s.db.Write(batch, nil)
	}
	return nil
}

// ListRecentTxs returns up to `limit` recent transaction IDs, newest first,
// starting from offset (0 = most recent).
func (s *Store) ListRecentTxs(offset, limit int) ([]types.Hash256, error) {
	total, err := s.GetTxCount()
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}

	var txids []types.Hash256
	// Walk backwards from newest.
	start := int(total) - 1 - offset
	if start < 0 {
		return nil, nil
	}
	for i := start; i >= 0 && len(txids) < limit; i-- {
		key := make([]byte, len(prefixTxIndex)+8)
		copy(key, prefixTxIndex)
		binary.BigEndian.PutUint64(key[len(prefixTxIndex):], uint64(i))
		val, err := s.db.Get(key, nil)
		if err != nil {
			continue
		}
		txids = append(txids, types.BytesToHash256(val))
	}
	return txids, nil
}
