package contracts

// Token contract storage layout:
//
// Slot 0: total supply
// Slot 1: owner (caller_id of deployer, set on first call)
// Slot 2: max supply (set on first call)
// Slot 1000 + (caller_id): balance of address
//
// Call data convention (via storage slots):
//   slot 100: function selector
//   slot 101: arg1
//   slot 102: arg2
//   slot 103: caller identifier (set by executor)
//   slot 200: return value

const (
	SlotTotalSupply = 0
	SlotOwner       = 1
	SlotMaxSupply   = 2
	SlotBalanceBase = 1000
)

const (
	FnMint      = 1
	FnTransfer  = 2
	FnBalanceOf = 3
)

// BuildTokenContract produces bytecode for a fungible token with an optional
// supply cap. The ownerID (caller_id of the deployer) and maxSupply are baked
// into the bytecode and stored in slots 1/2 on first call. Anyone can mint.
// If maxSupply > 0, minting fails when it would exceed the cap.
// If maxSupply == 0, minting is unlimited.
func BuildTokenContract(ownerID, maxSupply uint64) []byte {
	mint := buildMintSection(maxSupply > 0)
	transfer := buildTransferSection()
	balanceOf := buildBalanceOfSection()

	sections := [][]byte{mint, transfer, balanceOf}
	selectors := []uint64{FnMint, FnTransfer, FnBalanceOf}

	preamble := buildTokenPreamble(ownerID, maxSupply)
	return buildWithPreamble(preamble, sections, selectors)
}

// buildTokenPreamble creates a self-init preamble that stores ownerID in slot 1
// and maxSupply in slot 2 on the first invocation (when slot 1 == 0).
func buildTokenPreamble(ownerID, maxSupply uint64) []byte {
	estimate := 30
	for iter := 0; iter < 10; iter++ {
		a := NewAsm()
		// if SLOAD(1) != 0, skip init (owner already set)
		a.Push(SlotOwner).Sload()
		a.Push(uint64(estimate)).Jumpi()
		// Init: store owner and max supply.
		a.Push(ownerID)
		a.Push(SlotOwner)
		a.Sstore()
		a.Push(maxSupply)
		a.Push(SlotMaxSupply)
		a.Sstore()
		if a.Len() == estimate {
			return a.Bytes()
		}
		estimate = a.Len()
	}
	panic("token preamble size did not converge")
}

// buildContractDispatch creates a dispatch table and concatenates sections.
func buildContractDispatch(sections [][]byte, selectors []uint64) []byte {
	// Iterative: start with estimate, converge.
	dSize := 256 // generous initial estimate
	for iter := 0; iter < 10; iter++ {
		d := NewAsm()
		offsets := make([]int, len(sections))
		cur := dSize
		for i := range sections {
			offsets[i] = cur
			cur += len(sections[i])
		}
		for i, sel := range selectors {
			d.Push(100).Sload().Push(sel).Eq().Push(uint64(offsets[i])).Jumpi()
		}
		d.Halt()

		if d.Len() == dSize {
			var code []byte
			code = append(code, d.Bytes()...)
			for _, s := range sections {
				code = append(code, s...)
			}
			return code
		}
		dSize = d.Len()
	}
	panic("dispatch offset calculation did not converge")
}

func buildMintSection(hasCap bool) []byte {
	a := NewAsm()
	// Note on VM stack ops:
	// SUB: pops a (top), pops b (second), pushes a - b
	// SSTORE: pops key (top), pops value (second)
	// So for SSTORE(key, val): push val first, then push key, then SSTORE.

	// Validate: amount > 0.
	a.Push(101).Sload().RequireNonZero()

	if hasCap {
		// Validate: totalSupply + amount <= maxSupply.
		// RequireLTE: stack [..., a, b] fails if a > b.
		a.Push(SlotTotalSupply).Sload() // [old_supply]
		a.Push(101).Sload()             // [old_supply, amount]
		a.Add()                         // [new_supply]
		a.Push(SlotMaxSupply).Sload()   // [new_supply, maxSupply]
		a.RequireLTE()                   // fails if new_supply > maxSupply
	}

	// new_supply = old_supply + amount
	a.Push(SlotTotalSupply).Sload() // [old_supply]
	a.Push(101).Sload()             // [old_supply, amount]
	a.Add()                         // [new_supply]
	a.Push(SlotTotalSupply)         // [new_supply, key=0]
	a.Sstore()                      // SSTORE(key=0, val=new_supply)

	// balance[caller] += amount
	a.Push(103).Sload().Push(SlotBalanceBase).Add().Sload() // [old_balance]
	a.Push(101).Sload()                                      // [old_balance, amount]
	a.Add()                                                   // [new_balance]
	a.Push(103).Sload().Push(SlotBalanceBase).Add()          // [new_balance, caller_slot]
	a.Sstore()                                                // SSTORE(caller_slot, new_balance)
	a.Halt()
	return a.Bytes()
}

func buildTransferSection() []byte {
	a := NewAsm()
	// from_slot = 1000 + SLOAD(103)
	// to_slot = 1000 + SLOAD(101)
	// amount = SLOAD(102)

	// Validate: amount > 0.
	a.Push(102).Sload().RequireNonZero()

	// Validate: from_balance >= amount (i.e. amount <= from_balance).
	a.Push(102).Sload()                                      // [amount]
	a.Push(103).Sload().Push(SlotBalanceBase).Add().Sload()  // [amount, from_balance]
	a.RequireLTE()                                            // fails if amount > from_balance

	// Deduct from sender: new_from = from_balance - amount.
	// SUB pops a (top), b (second), pushes a - b.
	// We want from_balance - amount, so from_balance must be on top.
	a.Push(102).Sload()                                      // [amount]
	a.Push(103).Sload().Push(SlotBalanceBase).Add().Sload()  // [amount, from_balance]
	a.Sub()                                                   // [from_balance - amount]
	a.Push(103).Sload().Push(SlotBalanceBase).Add()          // [new_from, from_slot]
	a.Sstore()

	// Credit recipient: new_to = to_balance + amount.
	a.Push(101).Sload().Push(SlotBalanceBase).Add().Sload() // [to_balance]
	a.Push(102).Sload()                                      // [to_balance, amount]
	a.Add()                                                   // [new_to_balance]
	a.Push(101).Sload().Push(SlotBalanceBase).Add()          // [new_to, to_slot]
	a.Sstore()

	a.Halt()
	return a.Bytes()
}

func buildBalanceOfSection() []byte {
	a := NewAsm()
	// balance = SLOAD(1000 + SLOAD(101))
	a.Push(101).Sload().Push(SlotBalanceBase).Add().Sload()
	// Store result in slot 200.
	a.Push(200)
	a.Sstore()
	a.Halt()
	return a.Bytes()
}
