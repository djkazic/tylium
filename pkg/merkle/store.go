package merkle

// NodeStore is the persistence backend for Merkle tree nodes.
type NodeStore interface {
	Get(key []byte) ([]byte, error)
	Put(key []byte, value []byte) error
	Delete(key []byte) error
}

// MemStore is an in-memory implementation of NodeStore for testing.
type MemStore struct {
	data map[string][]byte
}

func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string][]byte)}
}

func (m *MemStore) Get(key []byte) ([]byte, error) {
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *MemStore) Put(key []byte, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[string(key)] = cp
	return nil
}

func (m *MemStore) Delete(key []byte) error {
	delete(m.data, string(key))
	return nil
}
