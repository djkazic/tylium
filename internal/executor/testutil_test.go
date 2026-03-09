package executor

import (
	"testing"

	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/types"
)

// testKey creates a deterministic key pair from a seed byte.
func testKey(t *testing.T, seed byte) (*crypto.PrivateKey, types.Address) {
	t.Helper()
	var keyBytes [32]byte
	keyBytes[31] = seed
	if seed == 0 {
		keyBytes[31] = 0xff
	}
	key, err := crypto.PrivateKeyFromBytes(keyBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	return key, key.Public().Address()
}

// signTx signs a transaction in place.
func signTx(t *testing.T, tx *types.Transaction, key *crypto.PrivateKey) {
	t.Helper()
	sig, err := crypto.Sign(tx.SigningHash(), key)
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
}
