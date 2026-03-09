package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/ripemd160"
)

// Address is a 20-byte account identifier derived from a public key.
type Address [20]byte

// ZeroAddress is the zero-value address, used for contract creation.
var ZeroAddress Address

// PubKeyToAddress derives an address from a compressed public key.
// Address = RIPEMD160(SHA256(pubkey)), matching Bitcoin's P2PKH scheme.
func PubKeyToAddress(pubkey []byte) Address {
	h := sha256.Sum256(pubkey)
	r := ripemd160.New()
	r.Write(h[:])
	var addr Address
	copy(addr[:], r.Sum(nil))
	return addr
}

func BytesToAddress(b []byte) Address {
	var addr Address
	if len(b) > 20 {
		b = b[:20]
	}
	copy(addr[20-len(b):], b)
	return addr
}

func (a Address) Bytes() []byte {
	return a[:]
}

func (a Address) Hex() string {
	return hex.EncodeToString(a[:])
}

func (a Address) IsZero() bool {
	return a == ZeroAddress
}

func (a Address) String() string {
	return fmt.Sprintf("0x%s", a.Hex())
}

// CallerID returns the low 8 bytes of the address as a uint64.
// This is used as the compact identifier in contract storage slots.
func (a Address) CallerID() uint64 {
	var id uint64
	for i := 12; i < 20; i++ {
		id = (id << 8) | uint64(a[i])
	}
	return id
}
