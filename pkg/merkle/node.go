package merkle

import (
	"encoding/binary"
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
)

// node is the interface for SMT nodes.
type node interface {
	hash(fn HashFunc) types.Hash256
}

// emptyNode represents an empty subtree.
type emptyNode struct{}

func (e emptyNode) hash(_ HashFunc) types.Hash256 {
	return types.ZeroHash
}

// leafNode stores a key-value pair at the leaves.
type leafNode struct {
	key   types.Hash256
	value []byte
}

func (l *leafNode) hash(fn HashFunc) types.Hash256 {
	// H(0x00 || key || value)
	buf := make([]byte, 0, 1+32+len(l.value))
	buf = append(buf, 0x00) // leaf prefix
	buf = append(buf, l.key[:]...)
	buf = append(buf, l.value...)
	return fn(buf)
}

// branchNode stores left and right children.
type branchNode struct {
	left  node
	right node
}

func (b *branchNode) hash(fn HashFunc) types.Hash256 {
	leftHash := b.left.hash(fn)
	rightHash := b.right.hash(fn)
	// H(0x01 || leftHash || rightHash)
	buf := make([]byte, 0, 1+32+32)
	buf = append(buf, 0x01) // branch prefix
	buf = append(buf, leftHash[:]...)
	buf = append(buf, rightHash[:]...)
	return fn(buf)
}

// hashNode is a lazy reference to a persisted node. It holds only the node's
// hash and is resolved from the NodeStore on first access during traversal.
type hashNode struct {
	h     types.Hash256
	store NodeStore
}

func (n *hashNode) hash(_ HashFunc) types.Hash256 {
	return n.h
}

// --- Node serialization ---
// Format:
//   Leaf:   [0x00][32-byte key][4-byte value length BE][value]
//   Branch: [0x01][32-byte left hash][32-byte right hash]

const (
	tagLeaf   = 0x00
	tagBranch = 0x01
)

func serializeNode(n node, fn HashFunc) []byte {
	switch n := n.(type) {
	case *leafNode:
		buf := make([]byte, 1+32+4+len(n.value))
		buf[0] = tagLeaf
		copy(buf[1:33], n.key[:])
		binary.BigEndian.PutUint32(buf[33:37], uint32(len(n.value)))
		copy(buf[37:], n.value)
		return buf
	case *branchNode:
		buf := make([]byte, 1+32+32)
		buf[0] = tagBranch
		lh := n.left.hash(fn)
		rh := n.right.hash(fn)
		copy(buf[1:33], lh[:])
		copy(buf[33:65], rh[:])
		return buf
	default:
		return nil
	}
}

func deserializeNode(data []byte, store NodeStore) (node, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("empty node data")
	}
	switch data[0] {
	case tagLeaf:
		if len(data) < 37 {
			return nil, fmt.Errorf("leaf too short")
		}
		var key types.Hash256
		copy(key[:], data[1:33])
		vlen := binary.BigEndian.Uint32(data[33:37])
		if uint32(len(data)-37) < vlen {
			return nil, fmt.Errorf("leaf value truncated")
		}
		value := make([]byte, vlen)
		copy(value, data[37:37+vlen])
		return &leafNode{key: key, value: value}, nil
	case tagBranch:
		if len(data) < 65 {
			return nil, fmt.Errorf("branch too short")
		}
		var lh, rh types.Hash256
		copy(lh[:], data[1:33])
		copy(rh[:], data[33:65])
		var left, right node
		if lh.IsZero() {
			left = emptyNode{}
		} else {
			left = &hashNode{h: lh, store: store}
		}
		if rh.IsZero() {
			right = emptyNode{}
		} else {
			right = &hashNode{h: rh, store: store}
		}
		return &branchNode{left: left, right: right}, nil
	default:
		return nil, fmt.Errorf("unknown node tag: %d", data[0])
	}
}
