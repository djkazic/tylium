package stress

import (
	"testing"

	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/types"
)

// testKey creates a deterministic key pair from a seed.
func testKey(t *testing.T, seed uint64) (*crypto.PrivateKey, types.Address) {
	t.Helper()
	var keyBytes [32]byte
	keyBytes[24] = byte(seed >> 56)
	keyBytes[25] = byte(seed >> 48)
	keyBytes[26] = byte(seed >> 40)
	keyBytes[27] = byte(seed >> 32)
	keyBytes[28] = byte(seed >> 24)
	keyBytes[29] = byte(seed >> 16)
	keyBytes[30] = byte(seed >> 8)
	keyBytes[31] = byte(seed)
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
