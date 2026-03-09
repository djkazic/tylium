package merkle

import "github.com/djkazic/tylium/pkg/types"

// Proof is a Merkle inclusion/exclusion proof for a key.
type Proof struct {
	Key      types.Hash256
	Value    []byte // nil for exclusion proof
	Siblings []types.Hash256
}

// Prove generates a Merkle proof for the given key.
func (t *Tree) Prove(key types.Hash256) (*Proof, error) {
	proof := &Proof{Key: key}
	t.prove(t.root, key, 0, proof)
	return proof, nil
}

func (t *Tree) prove(n node, key types.Hash256, depth int, proof *Proof) {
	switch n := n.(type) {
	case emptyNode:
		return
	case *hashNode:
		resolved, err := t.resolve(n)
		if err != nil {
			return
		}
		t.prove(resolved, key, depth, proof)
	case *leafNode:
		if n.key == key {
			proof.Value = n.value
		}
		return
	case *branchNode:
		if getBit(key, depth) == 0 {
			proof.Siblings = append(proof.Siblings, n.right.hash(t.hash))
			t.prove(n.left, key, depth+1, proof)
		} else {
			proof.Siblings = append(proof.Siblings, n.left.hash(t.hash))
			t.prove(n.right, key, depth+1, proof)
		}
	}
}

// VerifyProof checks a Merkle proof against a known root.
func VerifyProof(root types.Hash256, proof *Proof, hashFunc HashFunc) bool {
	var currentHash types.Hash256
	if proof.Value == nil {
		currentHash = types.ZeroHash
	} else {
		buf := make([]byte, 0, 1+32+len(proof.Value))
		buf = append(buf, 0x00)
		buf = append(buf, proof.Key[:]...)
		buf = append(buf, proof.Value...)
		currentHash = hashFunc(buf)
	}

	// Walk siblings from leaf to root.
	for i := len(proof.Siblings) - 1; i >= 0; i-- {
		depth := i
		sibling := proof.Siblings[i]
		buf := make([]byte, 0, 1+32+32)
		buf = append(buf, 0x01)
		if getBit(proof.Key, depth) == 0 {
			buf = append(buf, currentHash[:]...)
			buf = append(buf, sibling[:]...)
		} else {
			buf = append(buf, sibling[:]...)
			buf = append(buf, currentHash[:]...)
		}
		currentHash = hashFunc(buf)
	}

	return currentHash == root
}
