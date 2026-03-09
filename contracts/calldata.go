package contracts

import "encoding/binary"

// PackCallData packs uint64 arguments into the tx.Data format
// expected by the executor (big-endian, 8 bytes per arg).
// The first arg should be the function selector.
func PackCallData(args ...uint64) []byte {
	data := make([]byte, len(args)*8)
	for i, arg := range args {
		binary.BigEndian.PutUint64(data[i*8:], arg)
	}
	return data
}
