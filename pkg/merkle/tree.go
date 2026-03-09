package merkle

import (
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
)

// Tree implements a sparse Merkle tree (SMT) with a fixed depth of 256 bits.
// Keys are Hash256 values; the path through the tree follows the key's bits.
//
// Trie nodes are persisted to the NodeStore via Flush(). On startup, LoadRoot()
// sets the root to a hashNode which is lazily resolved from the store during
// traversal.
type Tree struct {
	root  node
	hash  HashFunc
	store NodeStore
}

// HashFunc computes a hash from data.
type HashFunc func(data []byte) types.Hash256

// DefaultHashFunc uses SHA-256.
func DefaultHashFunc(data []byte) types.Hash256 {
	return types.Sha256(data)
}

// NewTree creates a new sparse Merkle tree with the given store.
func NewTree(store NodeStore) *Tree {
	return &Tree{
		root:  emptyNode{},
		hash:  DefaultHashFunc,
		store: store,
	}
}

// Root returns the root hash of the tree.
func (t *Tree) Root() types.Hash256 {
	return t.root.hash(t.hash)
}

// LoadRoot sets the tree root to a lazy hashNode that will be resolved from
// the store on first access. Call this on startup with the persisted state root.
func (t *Tree) LoadRoot(root types.Hash256) {
	if root.IsZero() {
		t.root = emptyNode{}
		return
	}
	t.root = &hashNode{h: root, store: t.store}
}

// Get retrieves the value for a key, or nil if not present.
func (t *Tree) Get(key types.Hash256) ([]byte, error) {
	val, err := t.get(t.root, key, 0)
	return val, err
}

// Set inserts or updates a key-value pair.
func (t *Tree) Set(key types.Hash256, value []byte) error {
	n, err := t.set(t.root, key, value, 0)
	if err != nil {
		return err
	}
	t.root = n
	return nil
}

// Delete removes a key from the tree.
func (t *Tree) Delete(key types.Hash256) error {
	n, err := t.delete(t.root, key, 0)
	if err != nil {
		return err
	}
	t.root = n
	return nil
}

// Flush persists all in-memory nodes to the NodeStore. Content-addressed:
// key = node hash, value = serialized node. Idempotent for unchanged nodes.
func (t *Tree) Flush() error {
	_, err := t.flush(t.root)
	return err
}

// flush recursively persists in-memory nodes, returning the node's hash.
func (t *Tree) flush(n node) (types.Hash256, error) {
	switch n := n.(type) {
	case emptyNode:
		return types.ZeroHash, nil
	case *hashNode:
		return n.h, nil // already persisted
	case *leafNode:
		h := n.hash(t.hash)
		data := serializeNode(n, t.hash)
		if err := t.store.Put(h[:], data); err != nil {
			return types.ZeroHash, fmt.Errorf("flush leaf: %w", err)
		}
		return h, nil
	case *branchNode:
		// Flush children first.
		if _, err := t.flush(n.left); err != nil {
			return types.ZeroHash, err
		}
		if _, err := t.flush(n.right); err != nil {
			return types.ZeroHash, err
		}
		h := n.hash(t.hash)
		data := serializeNode(n, t.hash)
		if err := t.store.Put(h[:], data); err != nil {
			return types.ZeroHash, fmt.Errorf("flush branch: %w", err)
		}
		return h, nil
	}
	return types.ZeroHash, nil
}

// resolve converts a hashNode into its concrete type by reading from the store.
func (t *Tree) resolve(n node) (node, error) {
	hn, ok := n.(*hashNode)
	if !ok {
		return n, nil
	}
	data, err := hn.store.Get(hn.h[:])
	if err != nil {
		return nil, fmt.Errorf("resolve node %s: %w", hn.h.Hex(), err)
	}
	if data == nil {
		return nil, fmt.Errorf("missing trie node %s", hn.h.Hex())
	}
	return deserializeNode(data, hn.store)
}

func (t *Tree) get(n node, key types.Hash256, depth int) ([]byte, error) {
	switch n := n.(type) {
	case emptyNode:
		return nil, nil
	case *hashNode:
		resolved, err := t.resolve(n)
		if err != nil {
			return nil, err
		}
		return t.get(resolved, key, depth)
	case *leafNode:
		if n.key == key {
			return n.value, nil
		}
		return nil, nil
	case *branchNode:
		if getBit(key, depth) == 0 {
			return t.get(n.left, key, depth+1)
		}
		return t.get(n.right, key, depth+1)
	}
	return nil, nil
}

func (t *Tree) set(n node, key types.Hash256, value []byte, depth int) (node, error) {
	switch n := n.(type) {
	case emptyNode:
		return &leafNode{key: key, value: value}, nil

	case *hashNode:
		resolved, err := t.resolve(n)
		if err != nil {
			return nil, err
		}
		return t.set(resolved, key, value, depth)

	case *leafNode:
		if n.key == key {
			return &leafNode{key: key, value: value}, nil
		}
		branch := &branchNode{left: emptyNode{}, right: emptyNode{}}
		branch = t.insert(branch, n.key, n.value, depth)
		branch = t.insert(branch, key, value, depth)
		return branch, nil

	case *branchNode:
		newBranch := &branchNode{left: n.left, right: n.right}
		if getBit(key, depth) == 0 {
			child, err := t.set(n.left, key, value, depth+1)
			if err != nil {
				return nil, err
			}
			newBranch.left = child
		} else {
			child, err := t.set(n.right, key, value, depth+1)
			if err != nil {
				return nil, err
			}
			newBranch.right = child
		}
		return newBranch, nil
	}
	return n, nil
}

func (t *Tree) insert(branch *branchNode, key types.Hash256, value []byte, depth int) *branchNode {
	if getBit(key, depth) == 0 {
		child, _ := t.set(branch.left, key, value, depth+1)
		branch.left = child
	} else {
		child, _ := t.set(branch.right, key, value, depth+1)
		branch.right = child
	}
	return branch
}

func (t *Tree) delete(n node, key types.Hash256, depth int) (node, error) {
	switch n := n.(type) {
	case emptyNode:
		return n, nil
	case *hashNode:
		resolved, err := t.resolve(n)
		if err != nil {
			return nil, err
		}
		return t.delete(resolved, key, depth)
	case *leafNode:
		if n.key == key {
			return emptyNode{}, nil
		}
		return n, nil
	case *branchNode:
		newBranch := &branchNode{left: n.left, right: n.right}
		if getBit(key, depth) == 0 {
			child, err := t.delete(n.left, key, depth+1)
			if err != nil {
				return nil, err
			}
			newBranch.left = child
		} else {
			child, err := t.delete(n.right, key, depth+1)
			if err != nil {
				return nil, err
			}
			newBranch.right = child
		}
		// Collapse if only one child remains.
		leftEmpty := isEmptyNode(newBranch.left)
		rightEmpty := isEmptyNode(newBranch.right)
		if leftEmpty && rightEmpty {
			return emptyNode{}, nil
		}
		if leftEmpty {
			if leaf, ok := newBranch.right.(*leafNode); ok {
				return leaf, nil
			}
		}
		if rightEmpty {
			if leaf, ok := newBranch.left.(*leafNode); ok {
				return leaf, nil
			}
		}
		return newBranch, nil
	}
	return n, nil
}

// getBit returns the bit at position pos in the hash (0 or 1).
// Bit 0 is the MSB of the first byte.
func getBit(h types.Hash256, pos int) byte {
	byteIdx := pos / 8
	bitIdx := uint(7 - pos%8)
	return (h[byteIdx] >> bitIdx) & 1
}

func isEmptyNode(n node) bool {
	_, ok := n.(emptyNode)
	return ok
}

// Snapshot captures the current tree root for later read-only queries.
// Safe to use concurrently with tree mutations since nodes are immutable.
type Snapshot struct {
	root  node
	hash  HashFunc
	store NodeStore
}

// TakeSnapshot returns a read-only snapshot of the current tree state.
func (t *Tree) TakeSnapshot() *Snapshot {
	return &Snapshot{root: t.root, hash: t.hash, store: t.store}
}

// Get retrieves the value for a key from the snapshot.
func (s *Snapshot) Get(key types.Hash256) []byte {
	val, _ := snapshotGet(s.root, key, 0, s.store)
	return val
}

// Root returns the root hash of the snapshot.
func (s *Snapshot) Root() types.Hash256 {
	return s.root.hash(s.hash)
}

func snapshotGet(n node, key types.Hash256, depth int, store NodeStore) ([]byte, error) {
	switch n := n.(type) {
	case emptyNode:
		return nil, nil
	case *hashNode:
		data, err := store.Get(n.h[:])
		if err != nil || data == nil {
			return nil, err
		}
		resolved, err := deserializeNode(data, store)
		if err != nil {
			return nil, err
		}
		return snapshotGet(resolved, key, depth, store)
	case *leafNode:
		if n.key == key {
			return n.value, nil
		}
		return nil, nil
	case *branchNode:
		if getBit(key, depth) == 0 {
			return snapshotGet(n.left, key, depth+1, store)
		}
		return snapshotGet(n.right, key, depth+1, store)
	}
	return nil, nil
}
