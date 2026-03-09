package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Hash256 is a 32-byte hash used throughout Tylium.
type Hash256 [32]byte

// ZeroHash is the zero-value hash.
var ZeroHash Hash256

// BytesToHash256 converts a byte slice to Hash256, left-padding if needed.
func BytesToHash256(b []byte) Hash256 {
	var h Hash256
	if len(b) > 32 {
		b = b[:32]
	}
	copy(h[32-len(b):], b)
	return h
}

// Sha256 computes the SHA-256 hash of data.
func Sha256(data []byte) Hash256 {
	return sha256.Sum256(data)
}

// DoubleSha256 computes SHA-256(SHA-256(data)), Bitcoin-style.
func DoubleSha256(data []byte) Hash256 {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

func (h Hash256) Bytes() []byte {
	return h[:]
}

func (h Hash256) Hex() string {
	return hex.EncodeToString(h[:])
}

func (h Hash256) IsZero() bool {
	return h == ZeroHash
}

func (h Hash256) String() string {
	return fmt.Sprintf("0x%s", h.Hex())
}
