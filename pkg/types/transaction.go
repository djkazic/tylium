package types

import "bytes"

// Transaction represents a Tylium transaction embedded in a Bitcoin L1 payload.
type Transaction struct {
	Version   uint8
	Nonce     uint64
	From      Address  // derived from signature recovery
	To        Address  // target account; zero address = contract creation
	Value     uint64   // photons to transfer
	GasPrice  uint64   // price per gas unit
	GasLimit  uint64   // maximum gas this tx may consume
	Priority  uint8    // 0-255 user-specified priority hint
	Data      []byte   // contract call data or init code
	Signature [64]byte // Schnorr signature
}

// TxID returns the canonical transaction identifier.
// TxID = DoubleSha256(version || nonce || to || value || gasPrice || gasLimit || priority || data)
// Signature is excluded to prevent malleability.
func (tx *Transaction) TxID() Hash256 {
	var buf []byte
	buf = append(buf, tx.Version)
	buf = append(buf, uint64ToBytes(tx.Nonce)...)
	buf = append(buf, tx.To[:]...)
	buf = append(buf, uint64ToBytes(tx.Value)...)
	buf = append(buf, uint64ToBytes(tx.GasPrice)...)
	buf = append(buf, uint64ToBytes(tx.GasLimit)...)
	buf = append(buf, tx.Priority)
	buf = append(buf, tx.Data...)
	return DoubleSha256(buf)
}

// SigningHash returns the hash that should be signed by the sender.
func (tx *Transaction) SigningHash() Hash256 {
	var buf []byte
	buf = append(buf, tx.Version)
	buf = append(buf, uint64ToBytes(tx.Nonce)...)
	buf = append(buf, tx.From[:]...)
	buf = append(buf, tx.To[:]...)
	buf = append(buf, uint64ToBytes(tx.Value)...)
	buf = append(buf, uint64ToBytes(tx.GasPrice)...)
	buf = append(buf, uint64ToBytes(tx.GasLimit)...)
	buf = append(buf, tx.Priority)
	buf = append(buf, tx.Data...)
	return DoubleSha256(buf)
}

// TxLess defines the deterministic transaction ordering.
// Higher gas price first, then higher priority, then lower txid.
func TxLess(i, j *Transaction) bool {
	if i.GasPrice != j.GasPrice {
		return i.GasPrice > j.GasPrice
	}
	if i.Priority != j.Priority {
		return i.Priority > j.Priority
	}
	iID := i.TxID()
	jID := j.TxID()
	return bytes.Compare(iID[:], jID[:]) < 0
}

func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
	return b
}
