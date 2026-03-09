package vm

import (
	"encoding/binary"
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
)

// ExecResult is the outcome of VM execution.
type ExecResult struct {
	GasUsed uint64
	Success bool
	Err     error
}

// VM is the Tylium virtual machine interpreter.
type VM struct{}

// Execute runs the bytecode in the given context.
func (vm *VM) Execute(ctx *Context) ExecResult {
	stack := NewStack()
	pc := 0
	gasUsed := uint64(0)

	for pc < len(ctx.Code) {
		op := Opcode(ctx.Code[pc])

		// Gas metering.
		cost, ok := GasCost[op]
		if !ok {
			return ExecResult{GasUsed: gasUsed, Success: false, Err: fmt.Errorf("invalid opcode 0x%02x at pc=%d", op, pc)}
		}
		if ctx.GasLeft < cost {
			return ExecResult{GasUsed: gasUsed, Success: false, Err: fmt.Errorf("out of gas at pc=%d", pc)}
		}
		ctx.GasLeft -= cost
		gasUsed += cost

		switch op {
		case OpHalt:
			return ExecResult{GasUsed: gasUsed, Success: true}

		case OpPush:
			pc++
			if pc >= len(ctx.Code) {
				return execErr(gasUsed, "PUSH: missing length byte")
			}
			n := int(ctx.Code[pc])
			pc++
			if n == 0 || n > 8 {
				return execErr(gasUsed, "PUSH: invalid length %d (must be 1-8)", n)
			}
			if pc+n > len(ctx.Code) {
				return execErr(gasUsed, "PUSH: data overflows code")
			}
			// Read up to 8 bytes as big-endian uint64.
			val := uint64(0)
			for i := 0; i < n; i++ {
				val = (val << 8) | uint64(ctx.Code[pc+i])
			}
			if err := stack.Push(val); err != nil {
				return execErr(gasUsed, "%s", err)
			}
			pc += n
			continue

		case OpPop:
			if _, err := stack.Pop(); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpDup:
			val, err := stack.Peek()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if err := stack.Push(val); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpSwap:
			a, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "SWAP: %s", err)
			}
			b, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "SWAP: %s", err)
			}
			stack.Push(a)
			stack.Push(b)

		case OpAdd:
			a, b, err := pop2(stack)
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if err := stack.Push(a + b); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpSub:
			a, b, err := pop2(stack)
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if err := stack.Push(a - b); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpMul:
			a, b, err := pop2(stack)
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if err := stack.Push(a * b); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpDiv:
			a, b, err := pop2(stack)
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if b == 0 {
				return execErr(gasUsed, "division by zero")
			}
			if err := stack.Push(a / b); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpEq:
			a, b, err := pop2(stack)
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			v := uint64(0)
			if a == b {
				v = 1
			}
			if err := stack.Push(v); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpLt:
			a, b, err := pop2(stack)
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			v := uint64(0)
			if a < b {
				v = 1
			}
			if err := stack.Push(v); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpNot:
			a, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			v := uint64(0)
			if a == 0 {
				v = 1
			}
			if err := stack.Push(v); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpJump:
			dest, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if int(dest) >= len(ctx.Code) {
				return execErr(gasUsed, "JUMP: destination %d out of bounds", dest)
			}
			pc = int(dest)
			continue

		case OpJumpi:
			dest, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			cond, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			if cond != 0 {
				if int(dest) >= len(ctx.Code) {
					return execErr(gasUsed, "JUMPI: destination %d out of bounds", dest)
				}
				pc = int(dest)
				continue
			}

		case OpSload:
			keyVal, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			var key types.Hash256
			binary.BigEndian.PutUint64(key[24:], keyVal)
			val := ctx.State.GetStorage(ctx.Target, key)
			result := binary.BigEndian.Uint64(val[24:])
			if err := stack.Push(result); err != nil {
				return execErr(gasUsed, "%s", err)
			}

		case OpSstore:
			keyVal, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			dataVal, err := stack.Pop()
			if err != nil {
				return execErr(gasUsed, "%s", err)
			}
			var key, val types.Hash256
			binary.BigEndian.PutUint64(key[24:], keyVal)
			binary.BigEndian.PutUint64(val[24:], dataVal)
			ctx.State.SetStorage(ctx.Target, key, val)

		default:
			return execErr(gasUsed, "unknown opcode 0x%02x", op)
		}

		pc++
	}

	// Fell off the end of code without HALT — implicit success.
	return ExecResult{GasUsed: gasUsed, Success: true}
}

func pop2(s *Stack) (uint64, uint64, error) {
	a, err := s.Pop()
	if err != nil {
		return 0, 0, err
	}
	b, err := s.Pop()
	if err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func execErr(gasUsed uint64, format string, args ...interface{}) ExecResult {
	return ExecResult{
		GasUsed: gasUsed,
		Success: false,
		Err:     fmt.Errorf(format, args...),
	}
}
