package contracts

import (
	"fmt"
	"testing"

	"github.com/djkazic/tylium/internal/vm"
	"github.com/djkazic/tylium/pkg/types"
)

// execAMM runs a single AMM contract call and returns the result.
func execAMM(code []byte, state *testState, contract types.Address, fn uint64, caller uint64, args ...uint64) vm.ExecResult {
	state.SetStorage(contract, slotKey(100), slotVal(fn))
	state.SetStorage(contract, slotKey(103), slotVal(caller))
	for i, arg := range args {
		state.SetStorage(contract, slotKey(uint64(101+i)), slotVal(arg))
	}
	ctx := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	return (&vm.VM{}).Execute(ctx)
}

// mustExecAMM runs execAMM and fatals on failure.
func mustExecAMM(t *testing.T, code []byte, state *testState, contract types.Address, fn uint64, caller uint64, args ...uint64) {
	t.Helper()
	result := execAMM(code, state, contract, fn, caller, args...)
	if !result.Success {
		t.Fatalf("AMM call (fn=%d) failed: %v", fn, result.Err)
	}
}

// setupPool creates a fresh AMM with liquidity for validation tests.
func setupPool(t *testing.T, resA, resB uint64) ([]byte, *testState, types.Address, uint64) {
	t.Helper()
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})
	alice := uint64(1)

	mustExecAMM(t, code, state, contract, AMMFnAddLiquidity, alice, resA, resB)
	return code, state, contract, alice
}

func TestAMMAddLiquidityAndSwap(t *testing.T) {
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})
	alice := uint64(1)

	v := &vm.VM{}

	// Add liquidity: 10000 token A, 10000 token B.
	state.SetStorage(contract, slotKey(100), slotVal(AMMFnAddLiquidity))
	state.SetStorage(contract, slotKey(101), slotVal(10000)) // amountA
	state.SetStorage(contract, slotKey(102), slotVal(10000)) // amountB
	state.SetStorage(contract, slotKey(103), slotVal(alice))

	ctx := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	result := v.Execute(ctx)
	if !result.Success {
		t.Fatalf("addLiquidity failed: %v", result.Err)
	}

	reserveA := readSlot(state, contract, AMMSlotReserveA)
	reserveB := readSlot(state, contract, AMMSlotReserveB)
	lpSupply := readSlot(state, contract, AMMSlotLPSupply)

	if reserveA != 10000 {
		t.Fatalf("expected reserveA 10000, got %d", reserveA)
	}
	if reserveB != 10000 {
		t.Fatalf("expected reserveB 10000, got %d", reserveB)
	}
	if lpSupply != 10000 {
		t.Fatalf("expected lpSupply 10000, got %d", lpSupply)
	}

	// Swap 1000 A for B (with 0.3% fee).
	// amountB_out = 10000 * (1000*997) / (10000*1000 + 1000*997) = 9970000000 / 10997000 = 906
	state.SetStorage(contract, slotKey(100), slotVal(AMMFnSwapAForB))
	state.SetStorage(contract, slotKey(101), slotVal(1000))
	state.SetStorage(contract, slotKey(103), slotVal(alice))

	ctx2 := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	result = v.Execute(ctx2)
	if !result.Success {
		t.Fatalf("swapAForB failed: %v", result.Err)
	}

	amountBOut := readSlot(state, contract, 200)
	if amountBOut != 906 {
		t.Fatalf("expected amountB_out 906, got %d", amountBOut)
	}

	// Check reserves updated.
	reserveA = readSlot(state, contract, AMMSlotReserveA)
	reserveB = readSlot(state, contract, AMMSlotReserveB)

	if reserveA != 11000 {
		t.Fatalf("expected reserveA 11000, got %d", reserveA)
	}
	if reserveB != 9094 { // 10000 - 906
		t.Fatalf("expected reserveB 9094, got %d", reserveB)
	}
}

func TestAMMSwapBForA(t *testing.T) {
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})

	v := &vm.VM{}

	// Add liquidity: 10000 A, 20000 B.
	state.SetStorage(contract, slotKey(100), slotVal(AMMFnAddLiquidity))
	state.SetStorage(contract, slotKey(101), slotVal(10000))
	state.SetStorage(contract, slotKey(102), slotVal(20000))
	state.SetStorage(contract, slotKey(103), slotVal(1))

	ctx := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	v.Execute(ctx)

	// Swap 2000 B for A (with 0.3% fee).
	// amountA_out = 10000 * (2000*997) / (20000*1000 + 2000*997) = 19940000000 / 21994000 = 906
	state.SetStorage(contract, slotKey(100), slotVal(AMMFnSwapBForA))
	state.SetStorage(contract, slotKey(101), slotVal(2000))
	state.SetStorage(contract, slotKey(103), slotVal(1))

	ctx2 := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	result := v.Execute(ctx2)
	if !result.Success {
		t.Fatalf("swapBForA failed: %v", result.Err)
	}

	amountAOut := readSlot(state, contract, 200)
	if amountAOut != 906 {
		t.Fatalf("expected amountA_out 906, got %d", amountAOut)
	}
}

func TestAMMRemoveLiquidity(t *testing.T) {
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})
	alice := uint64(1)

	v := &vm.VM{}

	// Add liquidity: 10000 A, 10000 B.
	state.SetStorage(contract, slotKey(100), slotVal(AMMFnAddLiquidity))
	state.SetStorage(contract, slotKey(101), slotVal(10000))
	state.SetStorage(contract, slotKey(102), slotVal(10000))
	state.SetStorage(contract, slotKey(103), slotVal(alice))

	ctx := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	v.Execute(ctx)

	// Remove half liquidity (5000 LP tokens).
	state.SetStorage(contract, slotKey(100), slotVal(AMMFnRemoveLiquidity))
	state.SetStorage(contract, slotKey(101), slotVal(5000))
	state.SetStorage(contract, slotKey(103), slotVal(alice))

	ctx2 := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	result := v.Execute(ctx2)
	if !result.Success {
		t.Fatalf("removeLiquidity failed: %v", result.Err)
	}

	amountA := readSlot(state, contract, 200)
	amountB := readSlot(state, contract, 201)

	if amountA != 5000 {
		t.Fatalf("expected amountA 5000, got %d", amountA)
	}
	if amountB != 5000 {
		t.Fatalf("expected amountB 5000, got %d", amountB)
	}

	// Reserves should be halved.
	resA := readSlot(state, contract, AMMSlotReserveA)
	resB := readSlot(state, contract, AMMSlotReserveB)
	if resA != 5000 || resB != 5000 {
		t.Fatalf("expected reserves 5000/5000, got %d/%d", resA, resB)
	}

	// LP balance should be halved.
	lpBal := readSlot(state, contract, AMMSlotLPBalBase+alice)
	if lpBal != 5000 {
		t.Fatalf("expected LP balance 5000, got %d", lpBal)
	}
}

// --- Validation tests ---

func TestAMMAddLiquidityZeroAmountA(t *testing.T) {
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})
	result := execAMM(code, state, contract, AMMFnAddLiquidity, 1, 0, 10000)
	if result.Success {
		t.Fatal("expected addLiquidity with amountA=0 to fail")
	}
}

func TestAMMAddLiquidityZeroAmountB(t *testing.T) {
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})
	result := execAMM(code, state, contract, AMMFnAddLiquidity, 1, 10000, 0)
	if result.Success {
		t.Fatal("expected addLiquidity with amountB=0 to fail")
	}
}

func TestAMMSwapOnEmptyPool(t *testing.T) {
	code := BuildAMMContract(997, 0)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xAA})

	// SwapAForB on empty pool (no reserves).
	result := execAMM(code, state, contract, AMMFnSwapAForB, 1, 1000)
	if result.Success {
		t.Fatal("expected swap on empty pool to fail")
	}

	// SwapBForA on empty pool.
	result = execAMM(code, state, contract, AMMFnSwapBForA, 1, 1000)
	if result.Success {
		t.Fatal("expected swap on empty pool to fail")
	}
}

func TestAMMSwapZeroAmount(t *testing.T) {
	code, state, contract, _ := setupPool(t, 10000, 10000)

	result := execAMM(code, state, contract, AMMFnSwapAForB, 1, 0)
	if result.Success {
		t.Fatal("expected swap with amountIn=0 to fail")
	}

	result = execAMM(code, state, contract, AMMFnSwapBForA, 1, 0)
	if result.Success {
		t.Fatal("expected swap with amountIn=0 to fail")
	}
}

func TestAMMRemoveLiquidityZero(t *testing.T) {
	code, state, contract, alice := setupPool(t, 10000, 10000)

	result := execAMM(code, state, contract, AMMFnRemoveLiquidity, alice, 0)
	if result.Success {
		t.Fatal("expected removeLiquidity with lpAmount=0 to fail")
	}
}

func TestAMMRemoveLiquidityExceedsBalance(t *testing.T) {
	code, state, contract, alice := setupPool(t, 10000, 10000)

	// Alice has 10000 LP tokens. Try removing 10001.
	result := execAMM(code, state, contract, AMMFnRemoveLiquidity, alice, 10001)
	if result.Success {
		t.Fatal("expected removeLiquidity exceeding balance to fail")
	}
}

func TestAMMRemoveLiquidityWrongCaller(t *testing.T) {
	code, state, contract, _ := setupPool(t, 10000, 10000)
	bob := uint64(2) // bob has 0 LP tokens

	result := execAMM(code, state, contract, AMMFnRemoveLiquidity, bob, 5000)
	if result.Success {
		t.Fatal("expected removeLiquidity from non-LP holder to fail")
	}
}

func TestAMMSwapTinyAmountZeroOutput(t *testing.T) {
	// With large reserves, a tiny swap should produce 0 output due to rounding.
	// amountOut = reserveB * (1 * 997) / (1000000 * 1000 + 1 * 997)
	//           = 1000000 * 997 / 1000000997 = 0 (integer division)
	code, state, contract, _ := setupPool(t, 1_000_000, 1_000_000)

	result := execAMM(code, state, contract, AMMFnSwapAForB, 1, 1)
	if result.Success {
		t.Fatal("expected swap with amountOut=0 (dust) to fail")
	}
}

func TestAMMValidSwapStillWorks(t *testing.T) {
	// Ensure a normal swap still succeeds after adding validation.
	code, state, contract, _ := setupPool(t, 10000, 10000)

	result := execAMM(code, state, contract, AMMFnSwapAForB, 1, 1000)
	if !result.Success {
		t.Fatalf("valid swap should succeed: %v", result.Err)
	}

	out := readSlot(state, contract, 200)
	if out != 906 {
		t.Fatalf("expected amountOut 906, got %d", out)
	}
}

func TestAMMValidRemoveLiquidityStillWorks(t *testing.T) {
	code, state, contract, alice := setupPool(t, 10000, 10000)

	result := execAMM(code, state, contract, AMMFnRemoveLiquidity, alice, 5000)
	if !result.Success {
		t.Fatalf("valid removeLiquidity should succeed: %v", result.Err)
	}

	resA := readSlot(state, contract, AMMSlotReserveA)
	resB := readSlot(state, contract, AMMSlotReserveB)
	if resA != 5000 || resB != 5000 {
		t.Fatalf("expected reserves 5000/5000, got %d/%d", resA, resB)
	}
}

func TestAMMRemoveAllLiquidity(t *testing.T) {
	code, state, contract, alice := setupPool(t, 10000, 10000)

	// Removing exactly all LP tokens should succeed (lpAmount == balance).
	result := execAMM(code, state, contract, AMMFnRemoveLiquidity, alice, 10000)
	if !result.Success {
		t.Fatalf("removing all liquidity should succeed: %v", result.Err)
	}

	resA := readSlot(state, contract, AMMSlotReserveA)
	resB := readSlot(state, contract, AMMSlotReserveB)
	if resA != 0 || resB != 0 {
		t.Fatalf("expected reserves 0/0, got %d/%d", resA, resB)
	}
}

func TestAMMConstantProductInvariant(t *testing.T) {
	code, state, contract, _ := setupPool(t, 100_000, 100_000)
	kBefore := uint64(100_000) * uint64(100_000)

	// Do 10 successive swaps alternating direction.
	for i := 0; i < 10; i++ {
		fn := AMMFnSwapAForB
		if i%2 == 1 {
			fn = AMMFnSwapBForA
		}
		mustExecAMM(t, code, state, contract, uint64(fn), 1, 1000)

		resA := readSlot(state, contract, AMMSlotReserveA)
		resB := readSlot(state, contract, AMMSlotReserveB)
		kAfter := resA * resB
		if kAfter < kBefore {
			t.Fatalf("swap %d: k decreased from %d to %d (resA=%d, resB=%d)", i, kBefore, kAfter, resA, resB)
		}
		kBefore = kAfter
	}
	fmt.Printf("  final k = %d (grew from fees)\n", kBefore)
}
