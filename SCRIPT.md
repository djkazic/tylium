# Tylium Script Language

A text assembly language for writing Tylium VM contracts. Compiles to bytecode that runs on the 14-opcode stack VM.

## Quick Example

```asm
; Simple counter contract
; Selector 1 = increment, Selector 2 = read

.const SLOT_COUNT 0
.const FN_INC 1
.const FN_READ 2

; --- Dispatch ---
PUSH 100
SLOAD               ; load function selector

PUSH FN_INC
EQ
PUSH @increment
JUMPI

PUSH 100
SLOAD
PUSH FN_READ
EQ
PUSH @read
JUMPI

HALT                 ; unknown selector, do nothing

; --- increment ---
increment:
  PUSH SLOT_COUNT
  SLOAD              ; [current_count]
  PUSH 1
  ADD                ; [current_count + 1]
  PUSH SLOT_COUNT
  SSTORE             ; store new count
  HALT

; --- read ---
read:
  PUSH SLOT_COUNT
  SLOAD              ; [current_count]
  PUSH 200
  SSTORE             ; write to return slot
  HALT
```

Compile it:
```go
import "github.com/djkazic/tylium/contracts/script"

bytecode, err := script.Compile(source)
// bytecode is ready to deploy via tylcli tx --data <hex>
```

## Syntax Reference

### Instructions

All 14 VM opcodes are available. Case insensitive.

| Instruction | Stack Effect | Description |
|-------------|-------------|-------------|
| `PUSH <val>` | → value | Push a value onto the stack |
| `POP` | value → | Discard top of stack |
| `ADD` | a, b → a+b | Addition (wrapping uint64) |
| `SUB` | a, b → a-b | Subtraction (top minus second) |
| `MUL` | a, b → a*b | Multiplication |
| `DIV` | a, b → a/b | Division (top divided by second, halts on zero) |
| `EQ` | a, b → 0/1 | Push 1 if equal |
| `LT` | a, b → 0/1 | Push 1 if top < second |
| `NOT` | a → 0/1 | Push 1 if top is zero |
| `JUMP` | dest → | Jump to code offset |
| `JUMPI` | cond, dest → | Jump if cond != 0 |
| `SLOAD` | key → value | Load from contract storage |
| `SSTORE` | value, key → | Store to contract storage |
| `HALT` | | Stop execution (success) |

### PUSH Values

`PUSH` accepts several value forms:

```asm
PUSH 42              ; decimal literal
PUSH 0xFF            ; hex literal
PUSH MY_CONST        ; named constant (defined with .const)
PUSH @my_label       ; label reference (resolved to byte offset)
```

### Constants

Define named values that can be reused:

```asm
.const SLOT_SUPPLY 0
.const SLOT_BAL_BASE 1000
.const FEE_NUM 997

PUSH SLOT_SUPPLY     ; same as PUSH 0
PUSH FEE_NUM         ; same as PUSH 997
```

### Labels

Define jump targets anywhere in the code:

```asm
PUSH @skip           ; push the byte offset of 'skip'
JUMP                 ; jump there

skip:                ; label definition
  HALT
```

Labels can be referenced before they're defined (forward references).

### Comments

Semicolons start a comment that runs to end of line:

```asm
; Full-line comment
PUSH 5          ; inline comment
SLOAD           ; load from slot 5
```

## Stack Convention

**This is critical to get right.** The VM pops the top of stack first.

For binary operations (`SUB`, `DIV`, `EQ`, `LT`):
- `a` = top of stack (popped first)
- `b` = second on stack (popped second)
- Result = `a op b`

This means for `SUB`: **top minus second**. For `x - y`, push `y` first, then `x`:

```asm
; Compute 100 - 30 = 70
PUSH 30              ; [30]         ← will be 'b'
PUSH 100             ; [30, 100]    ← will be 'a' (top)
SUB                  ; [70]         ← a - b = 100 - 30
```

For `SSTORE`: pops key (top), then value (second):

```asm
; Store value 42 in slot 0
PUSH 42              ; [42]         ← value
PUSH 0               ; [42, 0]     ← key (top)
SSTORE               ; []           ← stored slot[0] = 42
```

For `JUMPI`: pops dest (top), then cond (second):

```asm
; Jump to @target if condition is true
PUSH 100
SLOAD                ; [selector]   ← condition
PUSH @target         ; [selector, target_offset]
JUMPI                ; if selector != 0, jump to target
```

## Storage Slot Conventions

The executor writes call data into slots before your code runs:

| Slot | Set By | Contents |
|------|--------|----------|
| 100 | executor | Function selector |
| 101 | executor | Argument 1 |
| 102 | executor | Argument 2 |
| 103 | executor | Caller identifier (low 8 bytes of address) |
| 104+ | executor | Additional arguments |
| 200 | contract | Return value 1 |
| 201 | contract | Return value 2 |
| 0-99 | contract | Contract's own persistent state |
| 1000+ | contract | Per-address mappings (e.g. balances) |

## Patterns

### Function Dispatch

Every contract needs a dispatch table to route calls:

```asm
.const FN_MINT 1
.const FN_TRANSFER 2
.const FN_BALANCE 3

; Load selector from slot 100
PUSH 100
SLOAD

; Check each selector
PUSH FN_MINT
EQ
PUSH @mint
JUMPI

; Need to reload selector (EQ consumed it)
PUSH 100
SLOAD
PUSH FN_TRANSFER
EQ
PUSH @transfer
JUMPI

PUSH 100
SLOAD
PUSH FN_BALANCE
EQ
PUSH @balance
JUMPI

HALT                     ; no match

mint:
  ; ...
  HALT

transfer:
  ; ...
  HALT

balance:
  ; ...
  HALT
```

Note: `EQ` consumes both values from the stack, so you need to reload the selector before each comparison.

### Reading Arguments

```asm
; Read amount from arg1 (slot 101)
PUSH 101
SLOAD                    ; [amount]

; Read caller identity
PUSH 103
SLOAD                    ; [caller_id]
```

### Per-Address Storage (Balances)

Use slot `1000 + caller_id` for per-address data:

```asm
.const SLOT_BAL_BASE 1000

; Load balance of caller
PUSH 103
SLOAD                    ; [caller_id]
PUSH SLOT_BAL_BASE
ADD                      ; [1000 + caller_id]
SLOAD                    ; [balance]

; Store new balance
PUSH 103
SLOAD
PUSH SLOT_BAL_BASE
ADD                      ; [balance_slot]
SSTORE                   ; pops key=balance_slot, value=new_balance
```

### Accumulate (e.g., total_supply += amount)

```asm
.const SLOT_SUPPLY 0

PUSH SLOT_SUPPLY
SLOAD                    ; [old_supply]
PUSH 101
SLOAD                    ; [old_supply, amount]
ADD                      ; [new_supply]
PUSH SLOT_SUPPLY         ; [new_supply, key=0]
SSTORE                   ; store
```

### Subtraction (e.g., balance -= amount)

Remember: `SUB` computes `top - second`. Push the subtrahend first:

```asm
; balance -= amount
PUSH 101
SLOAD                    ; [amount]         ← will be second (b)

PUSH 103
SLOAD
PUSH 1000
ADD
SLOAD                    ; [amount, balance] ← balance is top (a)

SUB                      ; [balance - amount]

; Store result back
PUSH 103
SLOAD
PUSH 1000
ADD
SSTORE
```

### Writing Return Values

Write results to slots 200+ for the caller to read:

```asm
; Return a value in slot 200
PUSH 200
SSTORE                   ; pops key=200, value=result
```

### Conditional Logic

```asm
; If value > 0, jump to process
PUSH 0                   ; [0]
PUSH 101
SLOAD                    ; [0, value]
LT                       ; [1 if 0 < value, else 0]
PUSH @process
JUMPI

; value was 0, halt
HALT

process:
  ; handle non-zero value
  HALT
```

## Complete Example: Fungible Token

```asm
; Fungible token with mint, transfer, and balanceOf
;
; Storage:
;   Slot 0: total supply
;   Slot 1000+id: balance of address

.const SLOT_SUPPLY 0
.const SLOT_BAL_BASE 1000
.const FN_MINT 1
.const FN_TRANSFER 2
.const FN_BALANCE_OF 3

; ============ Dispatch ============
PUSH 100
SLOAD
PUSH FN_MINT
EQ
PUSH @mint
JUMPI

PUSH 100
SLOAD
PUSH FN_TRANSFER
EQ
PUSH @transfer
JUMPI

PUSH 100
SLOAD
PUSH FN_BALANCE_OF
EQ
PUSH @balance_of
JUMPI

HALT

; ============ mint(amount=slot101) ============
; Adds to total supply and caller's balance.
mint:
  ; supply += amount
  PUSH SLOT_SUPPLY
  SLOAD
  PUSH 101
  SLOAD
  ADD
  PUSH SLOT_SUPPLY
  SSTORE

  ; balance[caller] += amount
  PUSH 103
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SLOAD              ; [old_balance]
  PUSH 101
  SLOAD              ; [old_balance, amount]
  ADD                ; [new_balance]
  PUSH 103
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SSTORE
  HALT

; ============ transfer(to=slot101, amount=slot102) ============
; Deducts from caller, credits recipient.
transfer:
  ; from_balance -= amount
  ; SUB: top - second, so push amount first, then balance on top
  PUSH 102
  SLOAD              ; [amount]
  PUSH 103
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SLOAD              ; [amount, from_balance]
  SUB                ; [from_balance - amount]
  PUSH 103
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SSTORE

  ; to_balance += amount
  PUSH 101
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SLOAD              ; [to_balance]
  PUSH 102
  SLOAD              ; [to_balance, amount]
  ADD                ; [new_to_balance]
  PUSH 101
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SSTORE
  HALT

; ============ balanceOf(addr=slot101) ============
; Returns balance in slot 200.
balance_of:
  PUSH 101
  SLOAD
  PUSH SLOT_BAL_BASE
  ADD
  SLOAD              ; [balance]
  PUSH 200
  SSTORE             ; return in slot 200
  HALT
```

## Gas Costs

Every instruction costs gas. Running out of gas halts execution and reverts all state changes (but gas is still consumed).

| Instruction | Gas |
|-------------|-----|
| PUSH | 2 |
| POP | 2 |
| ADD, SUB | 3 |
| MUL, DIV | 5 |
| EQ, LT, NOT | 3 |
| JUMP | 8 |
| JUMPI | 10 |
| SLOAD | 200 |
| SSTORE | 5000 |
| HALT | 0 |

Storage operations are expensive. Minimize SLOAD/SSTORE calls where possible.

## Security Notes

- **No reentrancy risk**: The VM has no CALL opcode, so contracts cannot invoke other contracts. Reentrancy is structurally impossible.
- **No overflow protection**: Arithmetic wraps at uint64. If you need safe math, check values before operating.
- **No underflow protection**: `SUB` wraps. If a user transfers more than their balance, it wraps to a huge number. Add checks if needed.
- **Integer division truncates**: `DIV` rounds toward zero. Design fee math to minimize precision loss (multiply before dividing).
- **Gas limit**: Always set a reasonable gas limit. Contracts that loop via JUMP can consume all gas.

## Compiling and Deploying

```go
package main

import (
    "encoding/hex"
    "fmt"
    "log"

    "github.com/djkazic/tylium/contracts/script"
)

func main() {
    source := `
.const SLOT_COUNT 0
PUSH SLOT_COUNT
SLOAD
PUSH 1
ADD
PUSH SLOT_COUNT
SSTORE
HALT
`
    bytecode, err := script.Compile(source)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(hex.EncodeToString(bytecode))
    // Use this hex as: tylcli tx --data <hex>
}
```

Or compile from a file with a small helper:
```bash
go run ./tools/compile.go < mycontract.tyl | tylcli tx --data $(cat -)
```
