package crypto

import (
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// PrivateKey wraps a secp256k1 private key.
type PrivateKey struct {
	key *secp256k1.PrivateKey
}

// PublicKey wraps a secp256k1 public key.
type PublicKey struct {
	key *secp256k1.PublicKey
}

// GenerateKey creates a new random private key.
func GenerateKey() (*PrivateKey, error) {
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return &PrivateKey{key: key}, nil
}

// PrivateKeyFromBytes deserializes a 32-byte private key.
func PrivateKeyFromBytes(b []byte) (*PrivateKey, error) {
	if len(b) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(b))
	}
	key := secp256k1.PrivKeyFromBytes(b)
	return &PrivateKey{key: key}, nil
}

// Public returns the corresponding public key.
func (pk *PrivateKey) Public() *PublicKey {
	return &PublicKey{key: pk.key.PubKey()}
}

// Bytes returns the 32-byte scalar representation.
func (pk *PrivateKey) Bytes() []byte {
	return pk.key.Serialize()
}

// Inner returns the underlying secp256k1 private key.
func (pk *PrivateKey) Inner() *secp256k1.PrivateKey {
	return pk.key
}

// CompressedBytes returns the 33-byte compressed public key.
func (pub *PublicKey) CompressedBytes() []byte {
	return pub.key.SerializeCompressed()
}

// Address derives the Tylium address from the public key.
func (pub *PublicKey) Address() types.Address {
	return types.PubKeyToAddress(pub.CompressedBytes())
}

// Inner returns the underlying secp256k1 public key.
func (pub *PublicKey) Inner() *secp256k1.PublicKey {
	return pub.key
}

// DecompressPubKey restores a public key from 33-byte compressed form.
func DecompressPubKey(data []byte) (*PublicKey, error) {
	key, err := secp256k1.ParsePubKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse pubkey: %w", err)
	}
	return &PublicKey{key: key}, nil
}
