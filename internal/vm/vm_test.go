package vm

import (
	"testing"

	"github.com/djkazic/tylium/pkg/types"
)

// mockState implements StateAccessor for testing.
type mockState struct {
	storage map[types.Address]map[types.Hash256]types.Hash256
	balance map[types.Address]uint64
}

func newMockState() *mockState {
	return &mockState{
		storage: make(map[types.Address]map[types.Hash256]types.Hash256),
		balance: make(map[types.Address]uint64),
	}
}

func (m *mockState) GetStorage(addr types.Address, key types.Hash256) types.Hash256 {
	if s, ok := m.storage[addr]; ok {
		return s[key]
	}
	return types.ZeroHash
}

func (m *mockState) SetStorage(addr types.Address, key, val types.Hash256) {
	if _, ok := m.storage[addr]; !ok {
		m.storage[addr] = make(map[types.Hash256]types.Hash256)
	}
	m.storage[addr][key] = val
}

func (m *mockState) GetBalance(addr types.Address) uint64 { return m.balance[addr] }
func (m *mockState) SetBalance(addr types.Address, v uint64) { m.balance[addr] = v }

func TestHalt(t *testing.T) {
	vm := &VM{}
	ctx := &Context{
		Code:    []byte{byte(OpHalt)},
		GasLeft: 10000,
		State:   newMockState(),
	}
	result := vm.Execute(ctx)
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Err)
	}
	if result.GasUsed != 0 {
		t.Fatalf("expected 0 gas for HALT, got %d", result.GasUsed)
	}
}

func TestPushAndAdd(t *testing.T) {
	// PUSH 3, PUSH 5, ADD, HALT
	code := []byte{
		byte(OpPush), 1, 3, // push 3
		byte(OpPush), 1, 5, // push 5
		byte(OpAdd),        // 3 + 5 = 8
		byte(OpHalt),
	}
	vm := &VM{}
	ctx := &Context{Code: code, GasLeft: 10000, State: newMockState()}
	result := vm.Execute(ctx)
	if !result.Success {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	// gas = PUSH(2) + PUSH(2) + ADD(3) + HALT(0) = 7
	if result.GasUsed != 7 {
		t.Fatalf("expected 7 gas, got %d", result.GasUsed)
	}
}

func TestArithmetic(t *testing.T) {
	tests := []struct {
		name   string
		a, b   byte
		op     Opcode
		expect uint64
	}{
		{"add", 10, 20, OpAdd, 30},
		{"sub", 20, 5, OpSub, 15},
		{"mul", 6, 7, OpMul, 42},
		{"div", 100, 5, OpDiv, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := []byte{
				byte(OpPush), 1, tt.a,
				byte(OpPush), 1, tt.b,
				byte(tt.op),
				byte(OpHalt),
			}
			vm := &VM{}
			ctx := &Context{Code: code, GasLeft: 10000, State: newMockState()}
			result := vm.Execute(ctx)
			if !result.Success {
				t.Fatalf("unexpected error: %v", result.Err)
			}
		})
	}
}

func TestDivByZero(t *testing.T) {
	// Stack is LIFO: push 0 first (divisor), then 10 (dividend).
	// Pop order: a=10, b=0 => 10/0 = error.
	code := []byte{
		byte(OpPush), 1, 0,
		byte(OpPush), 1, 10,
		byte(OpDiv),
	}
	vm := &VM{}
	ctx := &Context{Code: code, GasLeft: 10000, State: newMockState()}
	result := vm.Execute(ctx)
	if result.Success {
		t.Fatal("expected failure on division by zero")
	}
}

func TestConditionalJump(t *testing.T) {
	// PUSH 1 (true), PUSH 7 (dest), JUMPI -> jumps to offset 7 which is HALT
	// offset 0: PUSH 1, 1  (3 bytes: 0,1,2)
	// offset 3: PUSH 1, 7  (3 bytes: 3,4,5)
	// offset 6: JUMPI       (1 byte: 6)
	// offset 7: HALT        (1 byte: 7)
	code := []byte{
		byte(OpPush), 1, 1,  // push 1 (condition true)
		byte(OpPush), 1, 7,  // push 7 (destination)
		byte(OpJumpi),       // conditional jump
		byte(OpHalt),        // target
	}
	vm := &VM{}
	ctx := &Context{Code: code, GasLeft: 10000, State: newMockState()}
	result := vm.Execute(ctx)
	if !result.Success {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}

func TestOutOfGas(t *testing.T) {
	code := []byte{
		byte(OpPush), 1, 1, // costs 2 gas
		byte(OpPush), 1, 2, // costs 2 gas — should fail
	}
	vm := &VM{}
	ctx := &Context{Code: code, GasLeft: 3, State: newMockState()}
	result := vm.Execute(ctx)
	if result.Success {
		t.Fatal("expected out of gas")
	}
}

func TestSloadSstore(t *testing.T) {
	// PUSH 42 (value), PUSH 1 (key), SSTORE, PUSH 1 (key), SLOAD, HALT
	code := []byte{
		byte(OpPush), 1, 42, // value
		byte(OpPush), 1, 1,  // key
		byte(OpSstore),
		byte(OpPush), 1, 1,  // key
		byte(OpSload),
		byte(OpHalt),
	}
	state := newMockState()
	target := types.BytesToAddress([]byte{0x01})
	vm := &VM{}
	ctx := &Context{
		Code:    code,
		GasLeft: 100000,
		Target:  target,
		State:   state,
	}
	result := vm.Execute(ctx)
	if !result.Success {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}

func TestComparison(t *testing.T) {
	// PUSH 5, PUSH 10, LT -> 1 (5 < 10)
	code := []byte{
		byte(OpPush), 1, 5,
		byte(OpPush), 1, 10,
		byte(OpLt),
		byte(OpHalt),
	}
	vm := &VM{}
	ctx := &Context{Code: code, GasLeft: 10000, State: newMockState()}
	result := vm.Execute(ctx)
	if !result.Success {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}

func TestNot(t *testing.T) {
	// PUSH 0, NOT -> 1
	code := []byte{
		byte(OpPush), 1, 0,
		byte(OpNot),
		byte(OpHalt),
	}
	vm := &VM{}
	ctx := &Context{Code: code, GasLeft: 10000, State: newMockState()}
	result := vm.Execute(ctx)
	if !result.Success {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}
