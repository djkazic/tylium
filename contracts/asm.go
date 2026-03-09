package contracts

import "github.com/djkazic/tylium/internal/vm"

// Asm is a simple bytecode assembler for the Tylium VM.
type Asm struct {
	code []byte
}

func NewAsm() *Asm {
	return &Asm{}
}

// Emit appends raw bytes.
func (a *Asm) Emit(b ...byte) *Asm {
	a.code = append(a.code, b...)
	return a
}

// Op emits a single opcode.
func (a *Asm) Op(op vm.Opcode) *Asm {
	return a.Emit(byte(op))
}

// Push emits PUSH with a uint64 value, using the minimum number of bytes.
func (a *Asm) Push(val uint64) *Asm {
	if val == 0 {
		return a.Emit(byte(vm.OpPush), 1, 0)
	}
	// Determine minimum byte count.
	n := 0
	v := val
	for v > 0 {
		n++
		v >>= 8
	}
	a.Emit(byte(vm.OpPush), byte(n))
	for i := n - 1; i >= 0; i-- {
		a.Emit(byte(val >> (uint(i) * 8)))
	}
	return a
}

// Halt emits HALT.
func (a *Asm) Halt() *Asm { return a.Op(vm.OpHalt) }

// Add emits ADD.
func (a *Asm) Add() *Asm { return a.Op(vm.OpAdd) }

// Sub emits SUB.
func (a *Asm) Sub() *Asm { return a.Op(vm.OpSub) }

// Mul emits MUL.
func (a *Asm) Mul() *Asm { return a.Op(vm.OpMul) }

// Div emits DIV.
func (a *Asm) Div() *Asm { return a.Op(vm.OpDiv) }

// Eq emits EQ.
func (a *Asm) Eq() *Asm { return a.Op(vm.OpEq) }

// Lt emits LT.
func (a *Asm) Lt() *Asm { return a.Op(vm.OpLt) }

// Not emits NOT.
func (a *Asm) Not() *Asm { return a.Op(vm.OpNot) }

// Sload emits SLOAD.
func (a *Asm) Sload() *Asm { return a.Op(vm.OpSload) }

// Sstore emits SSTORE.
func (a *Asm) Sstore() *Asm { return a.Op(vm.OpSstore) }

// Jump emits JUMP (expects destination on stack).
func (a *Asm) Jump() *Asm { return a.Op(vm.OpJump) }

// Jumpi emits JUMPI (conditional jump).
func (a *Asm) Jumpi() *Asm { return a.Op(vm.OpJumpi) }

// Pop emits POP.
func (a *Asm) Pop() *Asm { return a.Op(vm.OpPop) }

// Dup emits DUP (duplicate top of stack).
func (a *Asm) Dup() *Asm { return a.Op(vm.OpDup) }

// Swap emits SWAP (swap top two stack elements).
func (a *Asm) Swap() *Asm { return a.Op(vm.OpSwap) }

// Label returns the current offset (for jump targets).
func (a *Asm) Label() int {
	return len(a.code)
}

// Bytes returns the assembled bytecode.
func (a *Asm) Bytes() []byte {
	out := make([]byte, len(a.code))
	copy(out, a.code)
	return out
}

// Len returns the current code length.
func (a *Asm) Len() int {
	return len(a.code)
}

// RequireNonZero emits code that fails if the top of stack is 0.
// Uses DIV(1, value) which triggers "division by zero" on 0.
// Consumes the value from the stack.
func (a *Asm) RequireNonZero() *Asm {
	// Stack: [..., value]
	a.Push(1) // [..., value, 1]
	// DIV pops a=1(top), b=value(second) → pushes 1/value.
	// If value == 0: division by zero error.
	a.Div() // [..., 1/value]
	a.Pop() // [...]
	return a
}

// RequireLTE emits code that fails if second > top (a > b).
// Stack: [..., a, b] → [...] (fails if a > b, i.e. a not <= b).
func (a *Asm) RequireLTE() *Asm {
	// LT: pops top=b, second=a → pushes 1 if b < a (violation).
	a.Lt()  // [..., 1_if_bad]
	a.Not() // [..., 0_if_bad, 1_if_ok]
	// RequireNonZero: fails if 0 (violation).
	a.Push(1)
	a.Div()
	a.Pop()
	return a
}
