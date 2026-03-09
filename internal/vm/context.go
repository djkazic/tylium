package vm

import "github.com/djkazic/tylium/pkg/types"

// MaxCodeSize is the maximum bytecode length for a contract.
const MaxCodeSize = 24 * 1024 // 24 KB

// StateAccessor is the interface the VM uses to read/write state.
type StateAccessor interface {
	GetStorage(addr types.Address, key types.Hash256) types.Hash256
	SetStorage(addr types.Address, key types.Hash256, val types.Hash256)
	GetBalance(addr types.Address) uint64
	SetBalance(addr types.Address, val uint64)
}

// Context holds the execution environment for a single contract call.
type Context struct {
	Caller  types.Address
	Target  types.Address
	Value   uint64
	GasLeft uint64
	Code    []byte
	State   StateAccessor
}
