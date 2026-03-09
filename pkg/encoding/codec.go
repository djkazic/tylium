package encoding

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
)

// Deterministic binary encoding for all Tylium types.
// Format: fixed-layout, big-endian, length-prefixed variable fields.
// No protobuf, no JSON — minimal L1 footprint.

var ErrShortBuffer = errors.New("encoding: buffer too short")
var ErrInvalidData = errors.New("encoding: invalid data")

// EncodeTx serializes a Transaction to deterministic binary form.
func EncodeTx(tx *types.Transaction) []byte {
	// version(1) + nonce(8) + from(20) + to(20) + value(8) + gasPrice(8) +
	// gasLimit(8) + priority(1) + dataLen(4) + data + sig(64)
	size := 1 + 8 + 20 + 20 + 8 + 8 + 8 + 1 + 4 + len(tx.Data) + 64
	buf := make([]byte, 0, size)

	buf = append(buf, tx.Version)
	buf = appendUint64(buf, tx.Nonce)
	buf = append(buf, tx.From[:]...)
	buf = append(buf, tx.To[:]...)
	buf = appendUint64(buf, tx.Value)
	buf = appendUint64(buf, tx.GasPrice)
	buf = appendUint64(buf, tx.GasLimit)
	buf = append(buf, tx.Priority)
	buf = appendUint32(buf, uint32(len(tx.Data)))
	buf = append(buf, tx.Data...)
	buf = append(buf, tx.Signature[:]...)

	return buf
}

// DecodeTx deserializes a Transaction from binary form.
func DecodeTx(data []byte) (*types.Transaction, error) {
	const minSize = 1 + 8 + 20 + 20 + 8 + 8 + 8 + 1 + 4 + 64 // 142 bytes
	if len(data) < minSize {
		return nil, fmt.Errorf("%w: need at least %d bytes, got %d", ErrShortBuffer, minSize, len(data))
	}

	tx := &types.Transaction{}
	off := 0

	tx.Version = data[off]
	off++

	tx.Nonce = binary.BigEndian.Uint64(data[off:])
	off += 8

	copy(tx.From[:], data[off:off+20])
	off += 20

	copy(tx.To[:], data[off:off+20])
	off += 20

	tx.Value = binary.BigEndian.Uint64(data[off:])
	off += 8

	tx.GasPrice = binary.BigEndian.Uint64(data[off:])
	off += 8

	tx.GasLimit = binary.BigEndian.Uint64(data[off:])
	off += 8

	tx.Priority = data[off]
	off++

	dataLen := binary.BigEndian.Uint32(data[off:])
	off += 4

	// Cap data field to prevent OOM from malformed envelopes.
	const maxTxDataSize = 1_000_000 // 1MB
	if dataLen > maxTxDataSize {
		return nil, fmt.Errorf("%w: data field %d bytes exceeds max %d", ErrInvalidData, dataLen, maxTxDataSize)
	}

	if uint32(len(data)-off) < dataLen+64 {
		return nil, fmt.Errorf("%w: data field overflows buffer", ErrShortBuffer)
	}

	if dataLen > 0 {
		tx.Data = make([]byte, dataLen)
		copy(tx.Data, data[off:off+int(dataLen)])
	}
	off += int(dataLen)

	copy(tx.Signature[:], data[off:off+64])

	return tx, nil
}

// EncodeAccount serializes an Account.
func EncodeAccount(acct *types.Account) []byte {
	// nonce(8) + balance(8) + codeHash(32) + storageRoot(32) = 80 bytes
	buf := make([]byte, 0, 80)
	buf = appendUint64(buf, acct.Nonce)
	buf = appendUint64(buf, acct.Balance)
	buf = append(buf, acct.CodeHash[:]...)
	buf = append(buf, acct.StorageRoot[:]...)
	return buf
}

// DecodeAccount deserializes an Account.
func DecodeAccount(data []byte) (*types.Account, error) {
	if len(data) < 80 {
		return nil, fmt.Errorf("%w: account needs 80 bytes, got %d", ErrShortBuffer, len(data))
	}
	acct := &types.Account{}
	acct.Nonce = binary.BigEndian.Uint64(data[0:8])
	acct.Balance = binary.BigEndian.Uint64(data[8:16])
	copy(acct.CodeHash[:], data[16:48])
	copy(acct.StorageRoot[:], data[48:80])
	return acct, nil
}

// EncodeCheckpoint serializes a Checkpoint.
func EncodeCheckpoint(cp *types.Checkpoint) []byte {
	// stateRoot(32) + prevChecksum(32) + txRoot(32) +
	// gasCollected(8) + minerPubKey(33) + minerNonce(32) + attestationHash(32) + sig(64) = 265
	buf := make([]byte, 0, 265)
	buf = append(buf, cp.StateRoot[:]...)
	buf = append(buf, cp.PrevChecksum[:]...)
	buf = append(buf, cp.TxRoot[:]...)
	buf = appendUint64(buf, cp.GasCollected)
	buf = append(buf, cp.MinerPubKey[:]...)
	buf = append(buf, cp.MinerNonce[:]...)
	buf = append(buf, cp.AttestationHash[:]...)
	buf = append(buf, cp.Signature[:]...)
	return buf
}

// DecodeCheckpoint deserializes a Checkpoint.
func DecodeCheckpoint(data []byte) (*types.Checkpoint, error) {
	if len(data) < 265 {
		return nil, fmt.Errorf("%w: checkpoint needs 265 bytes, got %d", ErrShortBuffer, len(data))
	}
	cp := &types.Checkpoint{}
	off := 0

	copy(cp.StateRoot[:], data[off:off+32])
	off += 32

	copy(cp.PrevChecksum[:], data[off:off+32])
	off += 32

	copy(cp.TxRoot[:], data[off:off+32])
	off += 32

	cp.GasCollected = binary.BigEndian.Uint64(data[off:])
	off += 8

	copy(cp.MinerPubKey[:], data[off:off+33])
	off += 33

	copy(cp.MinerNonce[:], data[off:off+32])
	off += 32

	copy(cp.AttestationHash[:], data[off:off+32])
	off += 32

	copy(cp.Signature[:], data[off:off+64])

	return cp, nil
}

func appendUint64(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return append(buf, b...)
}

func appendUint32(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return append(buf, b...)
}
