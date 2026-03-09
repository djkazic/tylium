package encoding

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
)

// Tylium L1 envelope format.
// All Tylium payloads on Bitcoin are wrapped in this envelope.

// Magic prefix identifies Tylium payloads in OP_RETURN data.
var Magic = [4]byte{'T', 'Y', 'L', 0x01}

// Envelope types.
const (
	EnvelopeTypeTx         uint8 = 0x01
	EnvelopeTypeCheckpoint uint8 = 0x02
	EnvelopeTypeDeposit    uint8 = 0x03
)

// Envelope wraps a Tylium payload for embedding in a Bitcoin transaction.
type Envelope struct {
	Magic   [4]byte
	Type    uint8
	Payload []byte
}

// EncodeEnvelope serializes an envelope for L1 embedding.
func EncodeEnvelope(typ uint8, payload []byte) []byte {
	buf := make([]byte, 0, 4+1+4+len(payload))
	buf = append(buf, Magic[:]...)
	buf = append(buf, typ)
	buf = appendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	return buf
}

// DecodeEnvelope parses a Tylium envelope from raw bytes.
func DecodeEnvelope(data []byte) (*Envelope, error) {
	if len(data) < 9 { // 4 magic + 1 type + 4 length
		return nil, fmt.Errorf("%w: envelope too short", ErrShortBuffer)
	}

	if !bytes.Equal(data[:4], Magic[:]) {
		return nil, fmt.Errorf("%w: invalid magic prefix", ErrInvalidData)
	}

	env := &Envelope{}
	copy(env.Magic[:], data[:4])
	env.Type = data[4]

	payloadLen := readUint32(data[5:9])
	if uint32(len(data)-9) < payloadLen {
		return nil, fmt.Errorf("%w: payload overflows buffer", ErrShortBuffer)
	}

	env.Payload = make([]byte, payloadLen)
	copy(env.Payload, data[9:9+payloadLen])

	return env, nil
}

// EncodeDeposit serializes a deposit payload: recipient address (20 bytes) + amount (8 bytes big-endian).
func EncodeDeposit(recipient types.Address, amount uint64) []byte {
	buf := make([]byte, 28) // 20 + 8
	copy(buf[:20], recipient[:])
	binary.BigEndian.PutUint64(buf[20:28], amount)
	return buf
}

// DecodeDeposit deserializes a deposit payload into a recipient address and amount.
func DecodeDeposit(data []byte) (types.Address, uint64, error) {
	if len(data) < 28 {
		return types.ZeroAddress, 0, fmt.Errorf("%w: deposit needs 28 bytes, got %d", ErrShortBuffer, len(data))
	}
	var addr types.Address
	copy(addr[:], data[:20])
	amount := binary.BigEndian.Uint64(data[20:28])
	return addr, amount, nil
}

func readUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
