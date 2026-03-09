// Package script provides a text assembly compiler for the Tylium VM.
//
// Source syntax:
//
//	; comment
//	.const NAME value        ; named constant (decimal or 0x hex)
//	label:                   ; jump target
//	PUSH <value>             ; value = decimal | 0xHex | constant | @label
//	ADD | SUB | MUL | DIV   ; arithmetic
//	EQ | LT | NOT           ; comparison / logic
//	JUMP | JUMPI             ; control flow
//	SLOAD | SSTORE           ; storage
//	POP | HALT               ; stack / halt
package script

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/djkazic/tylium/internal/vm"
)

// opcodeTable maps upper-case mnemonic to opcode byte.
var opcodeTable = map[string]byte{
	"HALT":   byte(vm.OpHalt),
	"POP":    byte(vm.OpPop),
	"ADD":    byte(vm.OpAdd),
	"SUB":    byte(vm.OpSub),
	"MUL":    byte(vm.OpMul),
	"DIV":    byte(vm.OpDiv),
	"EQ":     byte(vm.OpEq),
	"LT":     byte(vm.OpLt),
	"NOT":    byte(vm.OpNot),
	"JUMP":   byte(vm.OpJump),
	"JUMPI":  byte(vm.OpJumpi),
	"SLOAD":  byte(vm.OpSload),
	"SSTORE": byte(vm.OpSstore),
}

// pushBytes returns the PUSH encoding for a uint64 value using minimum bytes.
// Format: 0x01 <length> <big-endian value bytes>
func pushBytes(val uint64) []byte {
	if val == 0 {
		return []byte{byte(vm.OpPush), 1, 0}
	}
	n := 0
	v := val
	for v > 0 {
		n++
		v >>= 8
	}
	buf := make([]byte, 2+n)
	buf[0] = byte(vm.OpPush)
	buf[1] = byte(n)
	for i := n - 1; i >= 0; i-- {
		buf[2+(n-1-i)] = byte(val >> (uint(i) * 8))
	}
	return buf
}

// pushSize returns how many bytes a PUSH <val> instruction occupies.
func pushSize(val uint64) int {
	if val == 0 {
		return 3 // opcode + length(1) + 0x00
	}
	n := 0
	v := val
	for v > 0 {
		n++
		v >>= 8
	}
	return 2 + n
}

// lineTokens strips comments and splits a line into whitespace-delimited tokens.
func lineTokens(line string) []string {
	if idx := strings.Index(line, ";"); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	return strings.Fields(line)
}

// parseValue parses a numeric literal (decimal or 0x hex).
func parseValue(s string) (uint64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid hex literal %q", s)
		}
		return v, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric literal %q", s)
	}
	return v, nil
}

// element is an intermediate token produced by parsing.
type element struct {
	kind string // "label", "push", "op"
	line int    // 1-based source line
	name string // label name (kind=="label") or push argument string (kind=="push")
	op   string // upper-cased mnemonic (kind=="op")
}

// Compile takes Tylium script source text and returns the corresponding
// VM bytecode. It uses a multi-pass approach: parse, resolve label offsets
// (iteratively since PUSH sizes depend on values), then emit.
func Compile(source string) ([]byte, error) {
	lines := strings.Split(source, "\n")

	consts := make(map[string]uint64)
	var elems []element

	// ── Parse ────────────────────────────────────────────────────────────

	for lineNo, raw := range lines {
		lineNum := lineNo + 1
		toks := lineTokens(raw)
		if toks == nil {
			continue
		}

		first := toks[0]

		// .const directive
		if strings.EqualFold(first, ".const") {
			if len(toks) != 3 {
				return nil, fmt.Errorf("line %d: .const requires NAME VALUE", lineNum)
			}
			name := toks[1]
			val, err := parseValue(toks[2])
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			if _, dup := consts[name]; dup {
				return nil, fmt.Errorf("line %d: duplicate constant %q", lineNum, name)
			}
			consts[name] = val
			continue
		}

		// Label (e.g. "mint:")
		if strings.HasSuffix(first, ":") {
			labelName := first[:len(first)-1]
			if labelName == "" {
				return nil, fmt.Errorf("line %d: empty label name", lineNum)
			}
			elems = append(elems, element{kind: "label", line: lineNum, name: labelName})
			toks = toks[1:]
			if len(toks) == 0 {
				continue
			}
			first = toks[0]
			// fall through to instruction parsing below
		}

		mnemonic := strings.ToUpper(first)

		if mnemonic == "PUSH" {
			if len(toks) < 2 {
				return nil, fmt.Errorf("line %d: PUSH requires an argument", lineNum)
			}
			elems = append(elems, element{kind: "push", line: lineNum, name: toks[1]})
			continue
		}

		if _, ok := opcodeTable[mnemonic]; !ok {
			return nil, fmt.Errorf("line %d: unknown instruction %q", lineNum, first)
		}
		elems = append(elems, element{kind: "op", line: lineNum, op: mnemonic})
	}

	// ── Resolve labels (iterative fixed-point) ───────────────────────────
	// Label offsets depend on PUSH sizes which depend on label values.
	// We iterate until offsets stabilise.

	labels := make(map[string]int)

	resolveArg := func(arg string) (uint64, bool) {
		if strings.HasPrefix(arg, "@") {
			off, ok := labels[arg[1:]]
			if !ok {
				return 0, false
			}
			return uint64(off), true
		}
		if v, ok := consts[arg]; ok {
			return v, true
		}
		v, err := parseValue(arg)
		if err != nil {
			return 0, false
		}
		return v, true
	}

	for iter := 0; iter < 20; iter++ {
		offset := 0
		changed := false
		for _, e := range elems {
			switch e.kind {
			case "label":
				prev, existed := labels[e.name]
				labels[e.name] = offset
				if !existed || prev != offset {
					changed = true
				}
			case "push":
				val, ok := resolveArg(e.name)
				if !ok {
					// Unresolved label on early iteration; assume max size.
					offset += 2 + 8
					continue
				}
				offset += pushSize(val)
			case "op":
				offset++
			}
		}
		if !changed {
			break
		}
	}

	// ── Validate all references ──────────────────────────────────────────

	// Check for duplicate labels.
	seenLabels := make(map[string]int)
	for _, e := range elems {
		if e.kind == "label" {
			if prev, ok := seenLabels[e.name]; ok {
				return nil, fmt.Errorf("line %d: duplicate label %q (first defined on line %d)", e.line, e.name, prev)
			}
			seenLabels[e.name] = e.line
		}
	}

	// ── Emit bytecode ────────────────────────────────────────────────────

	var code []byte
	for _, e := range elems {
		switch e.kind {
		case "label":
			// no bytes
		case "push":
			arg := e.name
			var val uint64
			if strings.HasPrefix(arg, "@") {
				lbl := arg[1:]
				off, ok := labels[lbl]
				if !ok {
					return nil, fmt.Errorf("line %d: undefined label %q", e.line, lbl)
				}
				val = uint64(off)
			} else if v, ok := consts[arg]; ok {
				val = v
			} else {
				v, err := parseValue(arg)
				if err != nil {
					return nil, fmt.Errorf("line %d: undefined constant or invalid value %q", e.line, arg)
				}
				val = v
			}
			code = append(code, pushBytes(val)...)
		case "op":
			code = append(code, opcodeTable[e.op])
		}
	}

	return code, nil
}
