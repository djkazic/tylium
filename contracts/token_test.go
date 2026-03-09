package contracts

import (
	"encoding/binary"
	"testing"

	"github.com/djkazic/tylium/internal/vm"
	"github.com/djkazic/tylium/pkg/types"
)

type testState struct {
	storage map[types.Address]map[types.Hash256]types.Hash256
	balance map[types.Address]uint64
}

func newTestState() *testState {
	return &testState{
		storage: make(map[types.Address]map[types.Hash256]types.Hash256),
		balance: make(map[types.Address]uint64),
	}
}

func (s *testState) GetStorage(addr types.Address, key types.Hash256) types.Hash256 {
	if m, ok := s.storage[addr]; ok {
		return m[key]
	}
	return types.ZeroHash
}

func (s *testState) SetStorage(addr types.Address, key, val types.Hash256) {
	if _, ok := s.storage[addr]; !ok {
		s.storage[addr] = make(map[types.Hash256]types.Hash256)
	}
	s.storage[addr][key] = val
}

func (s *testState) GetBalance(addr types.Address) uint64 { return s.balance[addr] }
func (s *testState) SetBalance(addr types.Address, v uint64) { s.balance[addr] = v }

func slotKey(n uint64) types.Hash256 {
	var h types.Hash256
	binary.BigEndian.PutUint64(h[24:], n)
	return h
}

func slotVal(n uint64) types.Hash256 {
	var h types.Hash256
	binary.BigEndian.PutUint64(h[24:], n)
	return h
}

func readSlot(s *testState, addr types.Address, slot uint64) uint64 {
	v := s.GetStorage(addr, slotKey(slot))
	return binary.BigEndian.Uint64(v[24:])
}

// execToken runs a single token contract call and returns the result.
func execToken(code []byte, state *testState, contract types.Address, fn uint64, caller uint64, args ...uint64) vm.ExecResult {
	state.SetStorage(contract, slotKey(100), slotVal(fn))
	state.SetStorage(contract, slotKey(103), slotVal(caller))
	for i, arg := range args {
		state.SetStorage(contract, slotKey(uint64(101+i)), slotVal(arg))
	}
	ctx := &vm.Context{Code: code, GasLeft: 1_000_000, Target: contract, State: state}
	return (&vm.VM{}).Execute(ctx)
}

func mustExecToken(t *testing.T, code []byte, state *testState, contract types.Address, fn uint64, caller uint64, args ...uint64) {
	t.Helper()
	result := execToken(code, state, contract, fn, caller, args...)
	if !result.Success {
		t.Fatalf("token call (fn=%d) failed: %v", fn, result.Err)
	}
}

func TestTokenMint(t *testing.T) {
	owner := uint64(42)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})

	mustExecToken(t, code, state, contract, FnMint, owner, 1000)

	supply := readSlot(state, contract, SlotTotalSupply)
	if supply != 1000 {
		t.Fatalf("expected supply 1000, got %d", supply)
	}

	callerBal := readSlot(state, contract, SlotBalanceBase+owner)
	if callerBal != 1000 {
		t.Fatalf("expected caller balance 1000, got %d", callerBal)
	}
}

func TestTokenMintUnlimited(t *testing.T) {
	owner := uint64(42)
	code := BuildTokenContract(owner, 0) // maxSupply=0 means unlimited
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})

	// Should be able to mint any amount.
	mustExecToken(t, code, state, contract, FnMint, owner, 1_000_000_000)
	mustExecToken(t, code, state, contract, FnMint, owner, 1_000_000_000)

	supply := readSlot(state, contract, SlotTotalSupply)
	if supply != 2_000_000_000 {
		t.Fatalf("expected supply 2000000000, got %d", supply)
	}
}

func TestTokenMintAnyoneCanMint(t *testing.T) {
	owner := uint64(42)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})

	// Non-owner should be able to mint.
	notOwner := uint64(99)
	result := execToken(code, state, contract, FnMint, notOwner, 1000)
	if !result.Success {
		t.Fatalf("anyone should be able to mint: %v", result.Err)
	}

	bal := readSlot(state, contract, SlotBalanceBase+notOwner)
	if bal != 1000 {
		t.Fatalf("expected minter balance 1000, got %d", bal)
	}
}

func TestTokenMintExceedsMaxSupply(t *testing.T) {
	owner := uint64(42)
	code := BuildTokenContract(owner, 500)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})

	// Mint 500 (at cap) should succeed.
	mustExecToken(t, code, state, contract, FnMint, owner, 500)

	// Mint 1 more should fail.
	result := execToken(code, state, contract, FnMint, owner, 1)
	if result.Success {
		t.Fatal("expected mint exceeding max supply to fail")
	}
}

func TestTokenMintZeroAmount(t *testing.T) {
	owner := uint64(42)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})

	result := execToken(code, state, contract, FnMint, owner, 0)
	if result.Success {
		t.Fatal("expected mint with amount=0 to fail")
	}
}

func TestTokenTransfer(t *testing.T) {
	owner := uint64(1)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})
	alice := owner
	bob := uint64(2)

	mustExecToken(t, code, state, contract, FnMint, alice, 1000)
	mustExecToken(t, code, state, contract, FnTransfer, alice, bob, 300)

	aliceBal := readSlot(state, contract, SlotBalanceBase+alice)
	bobBal := readSlot(state, contract, SlotBalanceBase+bob)

	if aliceBal != 700 {
		t.Fatalf("expected alice balance 700, got %d", aliceBal)
	}
	if bobBal != 300 {
		t.Fatalf("expected bob balance 300, got %d", bobBal)
	}
}

func TestTokenTransferInsufficientBalance(t *testing.T) {
	owner := uint64(1)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})
	bob := uint64(2)

	mustExecToken(t, code, state, contract, FnMint, owner, 1000)

	// Try transferring more than balance.
	result := execToken(code, state, contract, FnTransfer, owner, bob, 1001)
	if result.Success {
		t.Fatal("expected transfer exceeding balance to fail")
	}
}

func TestTokenTransferZeroAmount(t *testing.T) {
	owner := uint64(1)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})
	bob := uint64(2)

	mustExecToken(t, code, state, contract, FnMint, owner, 1000)

	result := execToken(code, state, contract, FnTransfer, owner, bob, 0)
	if result.Success {
		t.Fatal("expected transfer with amount=0 to fail")
	}
}

func TestTokenBalanceOf(t *testing.T) {
	owner := uint64(1)
	code := BuildTokenContract(owner, 1_000_000)
	state := newTestState()
	contract := types.BytesToAddress([]byte{0xCC})

	mustExecToken(t, code, state, contract, FnMint, owner, 500)

	// Query balance.
	mustExecToken(t, code, state, contract, FnBalanceOf, owner, owner)

	bal := readSlot(state, contract, 200)
	if bal != 500 {
		t.Fatalf("expected balance 500, got %d", bal)
	}
}
