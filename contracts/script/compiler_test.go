package script

import (
	"bytes"
	"strings"
	"testing"

	"github.com/djkazic/tylium/internal/vm"
)

// helper: build expected PUSH encoding using the same logic as contracts/asm.go
func push(val uint64) []byte {
	return pushBytes(val)
}

func TestSimplePushHalt(t *testing.T) {
	src := `PUSH 42
HALT`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	want := append(push(42), byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestArithmetic(t *testing.T) {
	src := `PUSH 10
PUSH 20
ADD
HALT`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	want = append(want, push(10)...)
	want = append(want, push(20)...)
	want = append(want, byte(vm.OpAdd))
	want = append(want, byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestConstants(t *testing.T) {
	src := `.const X 42
PUSH X
HALT`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	want := append(push(42), byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestLabelsAndForwardJump(t *testing.T) {
	// PUSH @end -> jumps past the ADD to HALT
	src := `PUSH @end
JUMP
ADD
end:
HALT`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}

	// PUSH @end occupies pushSize(endOffset) bytes.
	// end offset = pushSize(endOffset) + 1(JUMP) + 1(ADD)
	// If endOffset fits in 1 byte: pushSize = 3, so endOffset = 3+1+1 = 5
	// pushSize(5) = 3, consistent.
	endOffset := uint64(3 + 1 + 1) // = 5
	var want []byte
	want = append(want, push(endOffset)...)
	want = append(want, byte(vm.OpJump))
	want = append(want, byte(vm.OpAdd))
	want = append(want, byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestCommentsAndBlankLines(t *testing.T) {
	src := `; this is a comment

PUSH 1 ; inline comment

; another comment
HALT
`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	want := append(push(1), byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestSloadSstore(t *testing.T) {
	src := `PUSH 0
SLOAD
PUSH 1
PUSH 99
SSTORE
HALT`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	want = append(want, push(0)...)
	want = append(want, byte(vm.OpSload))
	want = append(want, push(1)...)
	want = append(want, push(99)...)
	want = append(want, byte(vm.OpSstore))
	want = append(want, byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestErrorUndefinedConstant(t *testing.T) {
	src := `PUSH UNKNOWN_CONST
HALT`
	_, err := Compile(src)
	if err == nil {
		t.Fatal("expected error for undefined constant")
	}
	if !strings.Contains(err.Error(), "UNKNOWN_CONST") {
		t.Fatalf("error should mention the constant name, got: %v", err)
	}
}

func TestErrorUndefinedLabel(t *testing.T) {
	src := `PUSH @nowhere
HALT`
	_, err := Compile(src)
	if err == nil {
		t.Fatal("expected error for undefined label")
	}
	if !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("error should mention the label name, got: %v", err)
	}
}

func TestErrorInvalidInstruction(t *testing.T) {
	src := `FOOBAR
HALT`
	_, err := Compile(src)
	if err == nil {
		t.Fatal("expected error for invalid instruction")
	}
	if !strings.Contains(err.Error(), "FOOBAR") {
		t.Fatalf("error should mention the bad instruction, got: %v", err)
	}
}

func TestMiniContract(t *testing.T) {
	// A simplified token mint dispatch:
	// Read function selector from storage slot 0, compare to FN_MINT (1),
	// if match jump to mint label, otherwise halt.
	src := `
.const SLOT_SELECTOR 0
.const FN_MINT 1
.const SLOT_SUPPLY 10

; Load function selector
PUSH SLOT_SELECTOR
SLOAD

; Check if it's mint
PUSH FN_MINT
EQ
PUSH @mint
JUMPI

; Default: halt
HALT

mint:
  ; Increment supply: load, add 1, store
  PUSH SLOT_SUPPLY
  SLOAD
  PUSH 1
  ADD
  PUSH SLOT_SUPPLY
  SSTORE
  HALT
`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}

	// Build expected bytecode manually.
	var want []byte
	want = append(want, push(0)...)    // PUSH SLOT_SELECTOR (0)
	want = append(want, byte(vm.OpSload))
	want = append(want, push(1)...)    // PUSH FN_MINT (1)
	want = append(want, byte(vm.OpEq))

	// mint label offset: we need to figure it out.
	// So far: push(0)=3, SLOAD=1, push(1)=3, EQ=1 = 8
	// Next: PUSH @mint = 3 bytes (if mint offset fits in 1 byte), JUMPI=1, HALT=1
	// mint offset = 8 + 3 + 1 + 1 = 13
	mintOffset := uint64(8 + 3 + 1 + 1)
	want = append(want, push(mintOffset)...) // PUSH @mint
	want = append(want, byte(vm.OpJumpi))
	want = append(want, byte(vm.OpHalt))

	// mint body
	want = append(want, push(10)...) // PUSH SLOT_SUPPLY
	want = append(want, byte(vm.OpSload))
	want = append(want, push(1)...) // PUSH 1
	want = append(want, byte(vm.OpAdd))
	want = append(want, push(10)...) // PUSH SLOT_SUPPLY
	want = append(want, byte(vm.OpSstore))
	want = append(want, byte(vm.OpHalt))

	if !bytes.Equal(got, want) {
		t.Fatalf("mini-contract bytecode mismatch\ngot  %x\nwant %x", got, want)
	}
}

func TestHexValues(t *testing.T) {
	src := `PUSH 0xFF
HALT`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	want := append(push(0xFF), byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestCaseInsensitivity(t *testing.T) {
	src := `push 5
add
halt`
	got, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	want = append(want, push(5)...)
	want = append(want, byte(vm.OpAdd))
	want = append(want, byte(vm.OpHalt))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}
