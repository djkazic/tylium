package executor

import "github.com/djkazic/tylium/pkg/types"

// Receipt records the outcome of executing a single transaction.
type Receipt struct {
	TxID    types.Hash256
	Height  uint64 // Tylium block height
	Success bool
	GasUsed uint64
	Err     string // empty on success
}
