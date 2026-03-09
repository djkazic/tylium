package merkle

import (
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// LevelDBStore implements NodeStore backed by LevelDB.
type LevelDBStore struct {
	db     *leveldb.DB
	prefix []byte // key prefix for namespace isolation
}

// NewLevelDBStore opens or creates a LevelDB database at the given path.
func NewLevelDBStore(path string, prefix []byte) (*LevelDBStore, error) {
	db, err := leveldb.OpenFile(path, &opt.Options{
		WriteBuffer:        64 * 1024 * 1024, // 64MB write buffer
		CompactionTableSize: 4 * 1024 * 1024,  // 4MB table size
	})
	if err != nil {
		return nil, fmt.Errorf("open leveldb %s: %w", path, err)
	}
	return &LevelDBStore{db: db, prefix: prefix}, nil
}

// NewLevelDBStoreFromDB wraps an existing LevelDB instance with a prefix.
func NewLevelDBStoreFromDB(db *leveldb.DB, prefix []byte) *LevelDBStore {
	return &LevelDBStore{db: db, prefix: prefix}
}

func (s *LevelDBStore) prefixKey(key []byte) []byte {
	if len(s.prefix) == 0 {
		return key
	}
	pk := make([]byte, len(s.prefix)+len(key))
	copy(pk, s.prefix)
	copy(pk[len(s.prefix):], key)
	return pk
}

func (s *LevelDBStore) Get(key []byte) ([]byte, error) {
	val, err := s.db.Get(s.prefixKey(key), nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return val, nil
}

func (s *LevelDBStore) Put(key []byte, value []byte) error {
	return s.db.Put(s.prefixKey(key), value, nil)
}

func (s *LevelDBStore) Delete(key []byte) error {
	return s.db.Delete(s.prefixKey(key), nil)
}

// Close closes the underlying LevelDB database.
func (s *LevelDBStore) Close() error {
	return s.db.Close()
}

// NewPrefixedRange returns a util.Range for iterating over keys with this store's prefix.
func (s *LevelDBStore) NewPrefixedRange() *util.Range {
	return util.BytesPrefix(s.prefix)
}
