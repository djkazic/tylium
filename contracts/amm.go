package contracts

// AMM (Automated Market Maker) contract using constant-product formula (x * y = k).
//
// The fee numerator is configurable per pool. It is stored in slot 5 and
// auto-initialized on the first call (self-init preamble). For example,
// feeNumerator=997 gives a 0.3% swap fee (matching Uniswap v2).
//
// Swap formula (integer math):
//   amountIn_with_fee = amountIn * feeNumerator
//   amountOut = reserveOut * amountIn_with_fee / (reserveIn * 1000 + amountIn_with_fee)
//
// Storage layout:
//   Slot 0: reserve of token A
//   Slot 1: reserve of token B
//   Slot 2: total LP token supply
//   Slot 3: token A identifier (reserved)
//   Slot 4: token B identifier (reserved)
//   Slot 5: fee numerator (e.g. 997 for 0.3%, 990 for 1%, 9970 for 0.3% with /10000)
//   Slot 1000+addr: LP token balance of addr
//
// Call data (via storage slots 100-104):
//   Slot 100: function selector
//   Slot 101: arg1
//   Slot 102: arg2
//   Slot 103: caller identifier
//   Slot 200: return value A
//   Slot 201: return value B
//
// VM stack convention:
//   SUB: pops a (top), b (second), pushes a - b
//   DIV: pops a (top), b (second), pushes a / b
//   SSTORE: pops key (top), value (second)

const (
	AMMSlotReserveA  = 0
	AMMSlotReserveB  = 1
	AMMSlotLPSupply  = 2
	AMMSlotTokenA    = 3 // callerID of the tokenA contract
	AMMSlotTokenB    = 4 // reserved (tokenB is always tyBTC)
	AMMSlotFeeNum    = 5
	AMMSlotLPBalBase = 1000

	// Settlement slots — written by AMM, read by executor for actual transfers.
	AMMSlotSettleDebitA  = 300 // tokenA to take from caller
	AMMSlotSettleCreditA = 301 // tokenA to give to caller
	AMMSlotSettleDebitB  = 302 // tyBTC to take from caller
	AMMSlotSettleCreditB = 303 // tyBTC to give to caller

	AMMFnAddLiquidity    = 1
	AMMFnRemoveLiquidity = 2
	AMMFnSwapAForB       = 3
	AMMFnSwapBForA       = 4
)

// BuildAMMContract produces bytecode for a constant-product AMM with the given
// fee numerator and tokenA identifier. tokenAID is the CallerID of the tokenA
// contract (low 8 bytes of its address). Token B is always tyBTC (native).
//
// The executor performs settlement after VM execution by reading slots 300-303.
// Common fee values: 997 (0.3%), 995 (0.5%), 990 (1%), 999 (0.1%).
func BuildAMMContract(feeNumerator, tokenAID uint64) []byte {
	addLiq := buildAddLiquidity()
	removeLiq := buildRemoveLiquidity()
	swapAB := buildSwapAForB()
	swapBA := buildSwapBForA()

	sections := [][]byte{addLiq, removeLiq, swapAB, swapBA}
	selectors := []uint64{AMMFnAddLiquidity, AMMFnRemoveLiquidity, AMMFnSwapAForB, AMMFnSwapBForA}

	preamble := buildAMMPreamble(feeNumerator, tokenAID)
	return buildWithPreamble(preamble, sections, selectors)
}

// buildWithPreamble builds preamble + dispatch + sections where the dispatch
// offsets account for the preamble length.
func buildWithPreamble(preambleSeed []byte, sections [][]byte, selectors []uint64) []byte {
	// Iterative: the preamble length may change as we account for its own size.
	preambleLen := len(preambleSeed)

	for iter := 0; iter < 10; iter++ {
		// Build dispatch with offsets shifted by preamble length.
		dSize := 256
		for dIter := 0; dIter < 10; dIter++ {
			d := NewAsm()
			offsets := make([]int, len(sections))
			cur := preambleLen + dSize
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
				code = append(code, preambleSeed...)
				code = append(code, d.Bytes()...)
				for _, s := range sections {
					code = append(code, s...)
				}
				return code
			}
			dSize = d.Len()
		}
	}
	panic("preamble+dispatch offset calculation did not converge")
}

// buildAMMPreamble creates a self-init preamble that stores feeNumerator in
// slot 5 and tokenAID in slot 3 on the first invocation (when slot 5 == 0).
func buildAMMPreamble(feeNumerator, tokenAID uint64) []byte {
	estimate := 30
	for iter := 0; iter < 10; iter++ {
		a := NewAsm()
		// if SLOAD(5) != 0, skip init
		a.Push(AMMSlotFeeNum).Sload()
		a.Push(uint64(estimate)).Jumpi()
		// Init: store fee numerator and tokenA ID.
		a.Push(feeNumerator)
		a.Push(AMMSlotFeeNum)
		a.Sstore()
		a.Push(tokenAID)
		a.Push(AMMSlotTokenA)
		a.Sstore()
		if a.Len() == estimate {
			return a.Bytes()
		}
		estimate = a.Len()
	}
	panic("AMM preamble size did not converge")
}

// addLiquidity(amountA=slot101, amountB=slot102)
func buildAddLiquidity() []byte {
	a := NewAsm()

	// Validate: amountA > 0, amountB > 0.
	a.Push(101).Sload().RequireNonZero()
	a.Push(102).Sload().RequireNonZero()

	// reserveA += amountA
	a.Push(AMMSlotReserveA).Sload() // [old_resA]
	a.Push(101).Sload()             // [old_resA, amountA]
	a.Add()                         // [new_resA]
	a.Push(AMMSlotReserveA)         // [new_resA, key]
	a.Sstore()

	// reserveB += amountB
	a.Push(AMMSlotReserveB).Sload()
	a.Push(102).Sload()
	a.Add()
	a.Push(AMMSlotReserveB)
	a.Sstore()

	// lpAmount = amountA (simplified)
	// lpSupply += lpAmount
	a.Push(AMMSlotLPSupply).Sload()
	a.Push(101).Sload()
	a.Add()
	a.Push(AMMSlotLPSupply)
	a.Sstore()

	// balance[caller] += lpAmount
	a.Push(103).Sload().Push(uint64(AMMSlotLPBalBase)).Add().Sload() // old LP balance
	a.Push(101).Sload()                                               // lpAmount
	a.Add()
	a.Push(103).Sload().Push(uint64(AMMSlotLPBalBase)).Add()
	a.Sstore()

	// Settlement: take amountA tokens and amountB tyBTC from caller.
	// Slots 301 (creditA) and 303 (creditB) are already 0 (executor clears them).
	a.Push(101).Sload().Push(AMMSlotSettleDebitA).Sstore()
	a.Push(102).Sload().Push(AMMSlotSettleDebitB).Sstore()

	a.Halt()
	return a.Bytes()
}

// removeLiquidity(lpAmount=slot101)
// amountA = reserveA * lpAmount / lpSupply
// amountB = reserveB * lpAmount / lpSupply
func buildRemoveLiquidity() []byte {
	a := NewAsm()

	// Validate: lpAmount > 0.
	a.Push(101).Sload().RequireNonZero()
	// Validate: lpAmount <= balance[caller].
	a.Push(101).Sload()                                               // [lpAmount]
	a.Push(103).Sload().Push(uint64(AMMSlotLPBalBase)).Add().Sload()  // [lpAmount, balance]
	a.RequireLTE()                                                     // fails if lpAmount > balance

	// DIV: pops a (top), b (second), pushes a / b.
	// We want (reserveA * lpAmount) / lpSupply.
	// So push lpSupply first (denominator, will be b), then numerator on top (a).

	// amountA = (reserveA * lpAmount) / lpSupply
	a.Push(AMMSlotLPSupply).Sload()  // [lpSupply] (denominator)
	a.Push(AMMSlotReserveA).Sload()  // [lpSupply, reserveA]
	a.Push(101).Sload()              // [lpSupply, reserveA, lpAmount]
	a.Mul()                          // [lpSupply, numerator]
	a.Div()                          // [numerator / lpSupply = amountA]
	a.Push(200)                      // [amountA, 200]
	a.Sstore()                       // store amountA in slot 200

	// amountB = (reserveB * lpAmount) / lpSupply
	a.Push(AMMSlotLPSupply).Sload()
	a.Push(AMMSlotReserveB).Sload()
	a.Push(101).Sload()
	a.Mul()
	a.Div()
	a.Push(201)
	a.Sstore()

	// reserveA -= amountA: want reserveA - amountA on stack.
	// SUB pops a (top), b (second), pushes a - b.
	// Push amountA (second), then reserveA (top). a=reserveA, b=amountA => reserveA - amountA.
	a.Push(200).Sload()              // [amountA]
	a.Push(AMMSlotReserveA).Sload()  // [amountA, reserveA]
	a.Sub()                          // [reserveA - amountA]
	a.Push(AMMSlotReserveA)
	a.Sstore()

	// reserveB -= amountB
	a.Push(201).Sload()
	a.Push(AMMSlotReserveB).Sload()
	a.Sub()
	a.Push(AMMSlotReserveB)
	a.Sstore()

	// lpSupply -= lpAmount
	a.Push(101).Sload()
	a.Push(AMMSlotLPSupply).Sload()
	a.Sub()
	a.Push(AMMSlotLPSupply)
	a.Sstore()

	// balance[caller] -= lpAmount
	a.Push(101).Sload()                                               // [lpAmount]
	a.Push(103).Sload().Push(uint64(AMMSlotLPBalBase)).Add().Sload()  // [lpAmount, old_balance]
	a.Sub()                                                            // [old_balance - lpAmount]
	a.Push(103).Sload().Push(uint64(AMMSlotLPBalBase)).Add()
	a.Sstore()

	// Settlement: give amountA tokens and amountB tyBTC to caller.
	// Slots 300 (debitA) and 302 (debitB) are already 0 (executor clears them).
	a.Push(200).Sload().Push(AMMSlotSettleCreditA).Sstore() // amountA from slot 200
	a.Push(201).Sload().Push(AMMSlotSettleCreditB).Sstore() // amountB from slot 201

	a.Halt()
	return a.Bytes()
}

// swapAForB(amountA=slot101)
// amountB_out = reserveB * (amountA * fee) / (reserveA * 1000 + amountA * fee)
// where fee = SLOAD(slot 5)
func buildSwapAForB() []byte {
	a := NewAsm()

	// Validate: amountIn > 0, reserves > 0.
	a.Push(101).Sload().RequireNonZero()
	a.Push(AMMSlotReserveA).Sload().RequireNonZero()
	a.Push(AMMSlotReserveB).Sload().RequireNonZero()

	// Load amountIn and fee once, DUP for reuse.
	// Compute amountIn_with_fee = amountIn * fee.
	a.Push(101).Sload()              // [amountIn]
	a.Push(AMMSlotFeeNum).Sload()    // [amountIn, fee]
	a.Mul()                          // [amountIn_wf]
	a.Dup()                          // [amountIn_wf, amountIn_wf]

	// denominator = reserveA * 1000 + amountIn_wf
	a.Push(AMMSlotReserveA).Sload()  // [amountIn_wf, amountIn_wf, reserveA]
	a.Push(1000).Mul()               // [amountIn_wf, amountIn_wf, reserveA*1000]
	a.Add()                          // [amountIn_wf, denominator]

	// numerator = reserveB * amountIn_wf
	a.Swap()                         // [denominator, amountIn_wf]
	a.Push(AMMSlotReserveB).Sload()  // [denominator, amountIn_wf, reserveB]
	a.Mul()                          // [denominator, numerator]

	// DIV: pops a (top), b (second), pushes a / b.
	a.Div()                          // [amountB_out]

	// Validate: amountOut > 0. DUP to keep it on stack.
	a.Dup().RequireNonZero()         // [amountB_out]

	// Store amountB_out in slot 200 (return value) — keep on stack via DUP.
	a.Dup()                          // [amountB_out, amountB_out]
	a.Push(200).Sstore()             // [amountB_out]

	// reserveB -= amountB_out (use the copy still on stack).
	a.Push(AMMSlotReserveB).Sload()  // [amountB_out, reserveB]
	a.Sub()                          // [new_reserveB]
	a.Push(AMMSlotReserveB).Sstore()

	// reserveA += amountIn
	a.Push(AMMSlotReserveA).Sload()
	a.Push(101).Sload()
	a.Add()
	a.Push(AMMSlotReserveA).Sstore()

	// Settlement: debitA = amountIn, creditB = amountOut.
	// Slots 301 (creditA) and 302 (debitB) are already 0 (executor clears them).
	a.Push(101).Sload().Push(AMMSlotSettleDebitA).Sstore()
	a.Push(200).Sload().Push(AMMSlotSettleCreditB).Sstore()

	a.Halt()
	return a.Bytes()
}

// swapBForA(amountB=slot101)
// amountA_out = reserveA * (amountB * fee) / (reserveB * 1000 + amountB * fee)
// where fee = SLOAD(slot 5)
func buildSwapBForA() []byte {
	a := NewAsm()

	// Validate: amountIn > 0, reserves > 0.
	a.Push(101).Sload().RequireNonZero()
	a.Push(AMMSlotReserveA).Sload().RequireNonZero()
	a.Push(AMMSlotReserveB).Sload().RequireNonZero()

	// Compute amountIn_with_fee = amountIn * fee (load once, DUP for reuse).
	a.Push(101).Sload()
	a.Push(AMMSlotFeeNum).Sload()
	a.Mul()                          // [amountIn_wf]
	a.Dup()                          // [amountIn_wf, amountIn_wf]

	// denominator = reserveB * 1000 + amountIn_wf
	a.Push(AMMSlotReserveB).Sload()
	a.Push(1000).Mul()
	a.Add()                          // [amountIn_wf, denominator]

	// numerator = reserveA * amountIn_wf
	a.Swap()                         // [denominator, amountIn_wf]
	a.Push(AMMSlotReserveA).Sload()  // [denominator, amountIn_wf, reserveA]
	a.Mul()                          // [denominator, numerator]

	a.Div()                          // [amountA_out]

	// Validate: amountOut > 0. DUP to keep on stack.
	a.Dup().RequireNonZero()         // [amountA_out]

	// Store amountA_out in slot 200 (return value) — keep on stack via DUP.
	a.Dup()                          // [amountA_out, amountA_out]
	a.Push(200).Sstore()             // [amountA_out]

	// reserveA -= amountA_out
	a.Push(AMMSlotReserveA).Sload()  // [amountA_out, reserveA]
	a.Sub()                          // [new_reserveA]
	a.Push(AMMSlotReserveA).Sstore()

	// reserveB += amountB
	a.Push(AMMSlotReserveB).Sload()
	a.Push(101).Sload()
	a.Add()
	a.Push(AMMSlotReserveB).Sstore()

	// Settlement: creditA = amountOut, debitB = amountIn.
	// Slots 300 (debitA) and 303 (creditB) are already 0 (executor clears them).
	a.Push(200).Sload().Push(AMMSlotSettleCreditA).Sstore()
	a.Push(101).Sload().Push(AMMSlotSettleDebitB).Sstore()

	a.Halt()
	return a.Bytes()
}

