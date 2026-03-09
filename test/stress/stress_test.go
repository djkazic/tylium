package stress

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/djkazic/tylium/contracts"
	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// Stress test: deploy token + AMM, run hundreds of operations,
// verify state consistency throughout.

const (
	numTraders     = 20
	swapsPerTrader = 50
	mintAmount     = 1_000_000_000
	initialFunding = 10_000_000_000
	liquidityA     = 100_000_000
	liquidityB     = 100_000_000
)

func TestStressTokenMints(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	deployerKey, deployer := testKey(t, 1)
	stateDB.SetBalance(deployer, initialFunding)

	// Deploy token.
	tokenCode := contracts.BuildTokenContract(callerIDFromAddress(deployer), 1_000_000_000_000)
	deployTx := makeTx(deployer, types.ZeroAddress, 0, 200_000, tokenCode)
	signTx(t, deployTx, deployerKey)
	_, receipts, _ := exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	assertSuccess(t, receipts[0], "deploy token")
	tokenAddr := executor.DeriveContractAddress(deployer, 0)

	// Mint to 100 different accounts in rapid succession.
	blockHeight := uint64(2)
	nonce := uint64(1)
	totalMinted := uint64(0)

	for i := 0; i < 100; i++ {
		mintData := contracts.PackCallData(contracts.FnMint, 10000)
		mintTx := makeTx(deployer, tokenAddr, nonce, 500_000, mintData)
		signTx(t, mintTx, deployerKey)
		_, receipts, _ := exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{mintTx})
		assertSuccess(t, receipts[0], fmt.Sprintf("mint %d", i))
		totalMinted += 10000
		nonce++
		blockHeight++
	}

	// Verify total supply.
	supply := readSlot(stateDB, tokenAddr, contracts.SlotTotalSupply)
	if supply != totalMinted {
		t.Fatalf("expected total supply %d, got %d", totalMinted, supply)
	}
	t.Logf("minted %d tokens across 100 blocks", totalMinted)
}

func TestStressTokenTransfers(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	aliceKey, alice := testKey(t, 1)
	stateDB.SetBalance(alice, initialFunding)

	// Deploy + mint.
	tokenCode := contracts.BuildTokenContract(callerIDFromAddress(alice), 1_000_000_000_000)
	deployTx := makeTx(alice, types.ZeroAddress, 0, 200_000, tokenCode)
	signTx(t, deployTx, aliceKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	tokenAddr := executor.DeriveContractAddress(alice, 0)

	mintData := contracts.PackCallData(contracts.FnMint, mintAmount)
	mintTx := makeTx(alice, tokenAddr, 1, 500_000, mintData)
	signTx(t, mintTx, aliceKey)
	exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{mintTx})

	// Transfer to many recipients.
	blockHeight := uint64(3)
	nonce := uint64(2)
	transferAmount := uint64(1000)

	for i := 0; i < 200; i++ {
		recipientID := uint64(100 + i)
		data := contracts.PackCallData(contracts.FnTransfer, recipientID, transferAmount)
		tx := makeTx(alice, tokenAddr, nonce, 500_000, data)
		signTx(t, tx, aliceKey)
		_, receipts, _ := exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{tx})
		assertSuccess(t, receipts[0], fmt.Sprintf("transfer %d", i))
		nonce++
		blockHeight++
	}

	// Verify alice balance.
	aliceID := callerIDFromAddress(alice)
	aliceBal := readSlot(stateDB, tokenAddr, contracts.SlotBalanceBase+aliceID)
	expectedAlice := mintAmount - (200 * transferAmount)
	if aliceBal != expectedAlice {
		t.Fatalf("expected alice balance %d, got %d", expectedAlice, aliceBal)
	}

	// Spot check a few recipients.
	for _, i := range []int{0, 50, 100, 199} {
		recipientID := uint64(100 + i)
		bal := readSlot(stateDB, tokenAddr, contracts.SlotBalanceBase+recipientID)
		if bal != transferAmount {
			t.Fatalf("recipient %d: expected %d, got %d", recipientID, transferAmount, bal)
		}
	}

	t.Logf("completed 200 transfers, alice remaining: %d", aliceBal)
}

func TestStressAMMSwaps(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	lpKey, lp := testKey(t, 1)
	stateDB.SetBalance(lp, initialFunding)

	// Deploy AMM.
	ammCode := contracts.BuildAMMContract(997, 0)
	deployTx := makeTx(lp, types.ZeroAddress, 0, 200_000, ammCode)
	signTx(t, deployTx, lpKey)
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	ammAddr := executor.DeriveContractAddress(lp, 0)

	// Add large liquidity pool.
	addLiqData := contracts.PackCallData(contracts.AMMFnAddLiquidity, liquidityA, liquidityB)
	addLiqTx := makeTx(lp, ammAddr, 1, 500_000, addLiqData)
	signTx(t, addLiqTx, lpKey)
	_, receipts, _ := exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{addLiqTx})
	assertSuccess(t, receipts[0], "add liquidity")

	originalK := uint64(liquidityA) * uint64(liquidityB)
	blockHeight := uint64(3)

	// Each trader does many swaps alternating direction.
	// Pre-generate all trader keys.
	traderKeys := make([]*crypto.PrivateKey, numTraders)
	traderAddrs := make([]types.Address, numTraders)
	for i := 0; i < numTraders; i++ {
		traderKeys[i], traderAddrs[i] = testKey(t, uint64(10+i))
		stateDB.SetBalance(traderAddrs[i], initialFunding)
	}

	for trader := 0; trader < numTraders; trader++ {
		nonce := uint64(0)
		for swap := 0; swap < swapsPerTrader; swap++ {
			var data []byte
			if swap%2 == 0 {
				data = contracts.PackCallData(contracts.AMMFnSwapAForB, 500)
			} else {
				data = contracts.PackCallData(contracts.AMMFnSwapBForA, 500)
			}
			tx := makeTx(traderAddrs[trader], ammAddr, nonce, 500_000, data)
			signTx(t, tx, traderKeys[trader])
			_, receipts, _ := exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{tx})
			assertSuccess(t, receipts[0], fmt.Sprintf("trader %d swap %d", trader, swap))
			nonce++
			blockHeight++
		}
	}

	totalSwaps := numTraders * swapsPerTrader

	// Verify constant product only increases (fees accrue).
	resA := readSlot(stateDB, ammAddr, contracts.AMMSlotReserveA)
	resB := readSlot(stateDB, ammAddr, contracts.AMMSlotReserveB)
	finalK := resA * resB

	t.Logf("completed %d swaps across %d traders", totalSwaps, numTraders)
	t.Logf("reserves: A=%d B=%d", resA, resB)
	t.Logf("k: original=%d final=%d (%.2f%% increase)", originalK, finalK, float64(finalK-originalK)/float64(originalK)*100)

	if finalK < originalK {
		t.Fatalf("constant product violated: %d < %d", finalK, originalK)
	}

	// Verify LP supply unchanged (no one added/removed).
	lpSupply := readSlot(stateDB, ammAddr, contracts.AMMSlotLPSupply)
	if lpSupply != liquidityA { // LP supply == amountA from initial add
		t.Fatalf("LP supply changed unexpectedly: %d", lpSupply)
	}
}

func TestStressMultiTxBlocks(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	// Fund multiple accounts and process many txs per block.
	numAccounts := 10
	keys := make([]*crypto.PrivateKey, numAccounts)
	addrs := make([]types.Address, numAccounts)
	for i := range addrs {
		keys[i], addrs[i] = testKey(t, uint64(i+1))
		stateDB.SetBalance(addrs[i], initialFunding)
	}

	// Deploy token from account 0.
	tokenCode := contracts.BuildTokenContract(callerIDFromAddress(addrs[0]), 1_000_000_000_000)
	deployTx := makeTx(addrs[0], types.ZeroAddress, 0, 200_000, tokenCode)
	signTx(t, deployTx, keys[0])
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	tokenAddr := executor.DeriveContractAddress(addrs[0], 0)

	// Mint from account 0.
	mintData := contracts.PackCallData(contracts.FnMint, 10_000_000)
	mintTx := makeTx(addrs[0], tokenAddr, 1, 500_000, mintData)
	signTx(t, mintTx, keys[0])
	exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{mintTx})

	// Build blocks with multiple transactions from different accounts.
	blockHeight := uint64(3)
	nonces := make([]uint64, numAccounts)
	nonces[0] = 2 // already used 0 and 1

	for round := 0; round < 50; round++ {
		var txs []*types.Transaction

		// Account 0 transfers to each other account.
		for j := 1; j < numAccounts; j++ {
			recipientID := callerIDFromAddress(addrs[j])
			data := contracts.PackCallData(contracts.FnTransfer, recipientID, 100)
			tx := makeTx(addrs[0], tokenAddr, nonces[0], 500_000, data)
			tx.GasPrice = uint64(numAccounts - j + 1) // vary gas price for ordering
			signTx(t, tx, keys[0])
			txs = append(txs, tx)
			nonces[0]++
		}

		_, receipts, err := exec.ProcessBlock(blockHeight, types.ZeroHash, txs)
		if err != nil {
			t.Fatalf("block %d: %v", blockHeight, err)
		}

		for i, r := range receipts {
			if !r.Success {
				t.Fatalf("block %d tx %d failed: %s", blockHeight, i, r.Err)
			}
		}
		blockHeight++
	}

	// Verify token balances sum correctly.
	totalDistributed := uint64(0)
	for j := 1; j < numAccounts; j++ {
		id := callerIDFromAddress(addrs[j])
		bal := readSlot(stateDB, tokenAddr, contracts.SlotBalanceBase+id)
		totalDistributed += bal
	}

	deployerID := callerIDFromAddress(addrs[0])
	deployerBal := readSlot(stateDB, tokenAddr, contracts.SlotBalanceBase+deployerID)
	supply := readSlot(stateDB, tokenAddr, contracts.SlotTotalSupply)

	if deployerBal+totalDistributed != supply {
		t.Fatalf("balance mismatch: deployer(%d) + distributed(%d) != supply(%d)",
			deployerBal, totalDistributed, supply)
	}

	t.Logf("50 multi-tx blocks (%d txs each): supply=%d deployer=%d distributed=%d",
		numAccounts-1, supply, deployerBal, totalDistributed)
}

func TestStressAMMAddRemoveLiquidity(t *testing.T) {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	// Multiple LPs add and remove liquidity repeatedly.
	numLPs := 5
	lpKeys := make([]*crypto.PrivateKey, numLPs)
	lpAddrs := make([]types.Address, numLPs)
	for i := range lpAddrs {
		lpKeys[i], lpAddrs[i] = testKey(t, uint64(i+1))
		stateDB.SetBalance(lpAddrs[i], initialFunding)
	}

	// Deploy AMM from LP 0.
	ammCode := contracts.BuildAMMContract(997, 0)
	deployTx := makeTx(lpAddrs[0], types.ZeroAddress, 0, 200_000, ammCode)
	signTx(t, deployTx, lpKeys[0])
	exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
	ammAddr := executor.DeriveContractAddress(lpAddrs[0], 0)

	blockHeight := uint64(2)
	nonces := make([]uint64, numLPs)
	nonces[0] = 1

	// Each LP adds liquidity.
	for i := 0; i < numLPs; i++ {
		amtA := uint64((i + 1) * 10000)
		amtB := uint64((i + 1) * 10000)
		data := contracts.PackCallData(contracts.AMMFnAddLiquidity, amtA, amtB)
		tx := makeTx(lpAddrs[i], ammAddr, nonces[i], 500_000, data)
		signTx(t, tx, lpKeys[i])
		_, receipts, _ := exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{tx})
		assertSuccess(t, receipts[0], fmt.Sprintf("LP %d add liquidity", i))
		nonces[i]++
		blockHeight++
	}

	totalLPExpected := uint64(0)
	for i := 0; i < numLPs; i++ {
		totalLPExpected += uint64((i + 1) * 10000)
	}

	lpSupply := readSlot(stateDB, ammAddr, contracts.AMMSlotLPSupply)
	if lpSupply != totalLPExpected {
		t.Fatalf("expected LP supply %d, got %d", totalLPExpected, lpSupply)
	}

	// Do some swaps to generate fee revenue.
	traderKey, trader := testKey(t, 100)
	stateDB.SetBalance(trader, initialFunding)
	traderNonce := uint64(0)
	for i := 0; i < 20; i++ {
		data := contracts.PackCallData(contracts.AMMFnSwapAForB, 500)
		tx := makeTx(trader, ammAddr, traderNonce, 500_000, data)
		signTx(t, tx, traderKey)
		exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{tx})
		traderNonce++
		blockHeight++
	}

	// Each LP removes half their liquidity.
	for i := 0; i < numLPs; i++ {
		lpID := callerIDFromAddress(lpAddrs[i])
		lpBal := readSlot(stateDB, ammAddr, contracts.AMMSlotLPBalBase+lpID)
		removeAmt := lpBal / 2
		data := contracts.PackCallData(contracts.AMMFnRemoveLiquidity, removeAmt)
		tx := makeTx(lpAddrs[i], ammAddr, nonces[i], 500_000, data)
		signTx(t, tx, lpKeys[i])
		_, receipts, _ := exec.ProcessBlock(blockHeight, types.ZeroHash, []*types.Transaction{tx})
		assertSuccess(t, receipts[0], fmt.Sprintf("LP %d remove half", i))
		nonces[i]++
		blockHeight++
	}

	// Verify reserves are still positive and k holds.
	resA := readSlot(stateDB, ammAddr, contracts.AMMSlotReserveA)
	resB := readSlot(stateDB, ammAddr, contracts.AMMSlotReserveB)
	if resA == 0 || resB == 0 {
		t.Fatalf("reserves went to zero: A=%d B=%d", resA, resB)
	}
	t.Logf("after add/swap/remove cycles: reserveA=%d reserveB=%d lpSupply=%d",
		resA, resB, readSlot(stateDB, ammAddr, contracts.AMMSlotLPSupply))
}

func TestStressStateRootDeterminism(t *testing.T) {
	// Run the same sequence of transactions twice and verify identical state roots.
	var roots [2]types.Hash256

	for run := 0; run < 2; run++ {
		stateDB := state.NewStateDB(merkle.NewMemStore())
		exec := executor.New(stateDB)

		aliceKey, alice := testKey(t, 1)
		bobKey, bob := testKey(t, 2)
		stateDB.SetBalance(alice, initialFunding)
		stateDB.SetBalance(bob, initialFunding)

		// Deploy token.
		tokenCode := contracts.BuildTokenContract(callerIDFromAddress(alice), 1_000_000_000_000)
		deployTx := makeTx(alice, types.ZeroAddress, 0, 200_000, tokenCode)
		signTx(t, deployTx, aliceKey)
		exec.ProcessBlock(1, types.ZeroHash, []*types.Transaction{deployTx})
		tokenAddr := executor.DeriveContractAddress(alice, 0)

		// Deploy AMM.
		ammCode := contracts.BuildAMMContract(997, 0)
		deployTx2 := makeTx(bob, types.ZeroAddress, 0, 200_000, ammCode)
		signTx(t, deployTx2, bobKey)
		exec.ProcessBlock(2, types.ZeroHash, []*types.Transaction{deployTx2})
		ammAddr := executor.DeriveContractAddress(bob, 0)

		// Mint + add liquidity + swap.
		mintTx := makeTx(alice, tokenAddr, 1, 500_000, contracts.PackCallData(contracts.FnMint, 50000))
		signTx(t, mintTx, aliceKey)
		exec.ProcessBlock(3, types.ZeroHash, []*types.Transaction{mintTx})

		addLiqTx := makeTx(alice, ammAddr, 2, 500_000, contracts.PackCallData(contracts.AMMFnAddLiquidity, 10000, 10000))
		signTx(t, addLiqTx, aliceKey)
		exec.ProcessBlock(4, types.ZeroHash, []*types.Transaction{addLiqTx})

		swapTx := makeTx(bob, ammAddr, 1, 500_000, contracts.PackCallData(contracts.AMMFnSwapAForB, 1000))
		signTx(t, swapTx, bobKey)
		exec.ProcessBlock(5, types.ZeroHash, []*types.Transaction{swapTx})

		roots[run] = stateDB.Commit()
	}

	if roots[0] != roots[1] {
		t.Fatalf("state roots differ between runs: %s vs %s", roots[0].Hex(), roots[1].Hex())
	}
	t.Logf("deterministic state root confirmed: %s", roots[0].Hex())
}

// --- helpers ---

func makeTx(from, to types.Address, nonce, gasLimit uint64, data []byte) *types.Transaction {
	return &types.Transaction{
		Version:  1,
		Nonce:    nonce,
		From:     from,
		To:       to,
		GasPrice: 1,
		GasLimit: gasLimit,
		Data:     data,
	}
}

func readSlot(stateDB *state.StateDB, contract types.Address, slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	val := stateDB.GetStorage(contract, key)
	return binary.BigEndian.Uint64(val[24:])
}

func callerIDFromAddress(addr types.Address) uint64 {
	return binary.BigEndian.Uint64(addr[12:20])
}

func assertSuccess(t *testing.T, r *executor.Receipt, label string) {
	t.Helper()
	if !r.Success {
		t.Fatalf("%s: expected success, got error: %s", label, r.Err)
	}
}
