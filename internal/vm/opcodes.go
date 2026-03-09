package vm

// Opcode represents a Tylium VM instruction.
type Opcode byte

const (
	OpHalt   Opcode = 0x00 // Stop execution, return success
	OpPush   Opcode = 0x01 // Push immediate bytes onto stack (1-byte length prefix)
	OpPop    Opcode = 0x02 // Discard top of stack
	OpAdd    Opcode = 0x10 // a + b (wrapping uint64)
	OpSub    Opcode = 0x11 // a - b (wrapping uint64)
	OpMul    Opcode = 0x12 // a * b (wrapping uint64)
	OpDiv    Opcode = 0x13 // a / b (halts on zero division)
	OpEq     Opcode = 0x20 // push 1 if a == b, else 0
	OpLt     Opcode = 0x21 // push 1 if a < b, else 0
	OpNot    Opcode = 0x22 // push 1 if top == 0, else 0
	OpJump   Opcode = 0x30 // unconditional jump to code offset
	OpJumpi  Opcode = 0x31 // conditional jump: pop cond, if nonzero jump
	OpDup    Opcode = 0x03 // duplicate top of stack
	OpSwap   Opcode = 0x04 // swap top two stack elements
	OpSload  Opcode = 0x40 // load from contract storage
	OpSstore Opcode = 0x41 // store to contract storage
)

// Gas costs for each opcode.
var GasCost = map[Opcode]uint64{
	OpHalt:   0,
	OpPush:   2,
	OpPop:    2,
	OpDup:    2,
	OpSwap:   2,
	OpAdd:    3,
	OpSub:    3,
	OpMul:    5,
	OpDiv:    5,
	OpEq:     3,
	OpLt:     3,
	OpNot:    3,
	OpJump:   8,
	OpJumpi:  10,
	OpSload:  50,
	OpSstore: 800,
}

var opcodeName = map[Opcode]string{
	OpHalt: "HALT", OpPush: "PUSH", OpPop: "POP", OpDup: "DUP", OpSwap: "SWAP",
	OpAdd: "ADD", OpSub: "SUB", OpMul: "MUL", OpDiv: "DIV",
	OpEq: "EQ", OpLt: "LT", OpNot: "NOT",
	OpJump: "JUMP", OpJumpi: "JUMPI",
	OpSload: "SLOAD", OpSstore: "SSTORE",
}

func (o Opcode) String() string {
	if name, ok := opcodeName[o]; ok {
		return name
	}
	return "UNKNOWN"
}
