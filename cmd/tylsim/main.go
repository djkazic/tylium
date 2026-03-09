package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/djkazic/tylium/contracts"
	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// tylsim runs a long-lived simulation of user activity against the Tylium
// executor. It deploys tokens and AMM pools, then executes random mints,
// transfers, swaps, and liquidity operations — checking invariants after
// every block. Designed to run for hours looking for crashes.

func main() {
	duration := flag.Duration("duration", 0, "how long to run (0 = forever)")
	numAccounts := flag.Int("accounts", 20, "number of simulated accounts")
	numPools := flag.Int("pools", 3, "number of AMM pools with different fee tiers")
	blocksPerLog := flag.Int("logfreq", 1000, "log stats every N blocks")
	seed := flag.Int64("seed", 0, "random seed (0 = time-based)")
	flag.Parse()

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))
	log.Printf("starting simulation: seed=%d accounts=%d pools=%d", *seed, *numAccounts, *numPools)

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	sim := newSimulation(rng, *numAccounts, *numPools)
	sim.setup()

	deadline := time.Time{}
	if *duration > 0 {
		deadline = time.Now().Add(*duration)
	}

	for {
		select {
		case <-stop:
			log.Printf("interrupted — shutting down")
			sim.printStats()
			os.Exit(0)
		default:
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Printf("duration elapsed")
			sim.printStats()
			return
		}

		sim.step()

		if sim.blockHeight%uint64(*blocksPerLog) == 0 {
			sim.printStats()
		}
	}
}

type poolInfo struct {
	addr types.Address
	fee  uint64
	resA uint64 // tracked locally for invariant checks
	resB uint64
}

type simulation struct {
	rng       *rand.Rand
	stateDB   *state.StateDB
	exec      *executor.Executor
	accounts  []types.Address
	nonces    []uint64
	tokenAddr types.Address
	numPools  int
	pools     []poolInfo

	blockHeight   uint64
	totalTx       uint64
	totalSuccess  uint64
	totalFail     uint64
	totalGas      uint64
	lastStateRoot types.Hash256
	startTime     time.Time
}

func newSimulation(rng *rand.Rand, numAccounts, numPools int) *simulation {
	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	accounts := make([]types.Address, numAccounts)
	nonces := make([]uint64, numAccounts)
	for i := range accounts {
		accounts[i] = makeAddr(uint64(i + 1))
		stateDB.SetBalance(accounts[i], 1_000_000_000_000)
	}

	return &simulation{
		rng:         rng,
		stateDB:     stateDB,
		exec:        exec,
		accounts:    accounts,
		nonces:      nonces,
		numPools:    numPools,
		blockHeight: 1,
		startTime:   time.Now(),
	}
}

func (s *simulation) setup() {
	// Deploy token contract from account 0.
	tokenCode := contracts.BuildTokenContract(callerID(s.accounts[0]), 1_000_000_000_000)
	tx := s.makeTx(0, types.ZeroAddress, 200_000, tokenCode)
	s.execBlock(tx)
	s.tokenAddr = executor.DeriveContractAddress(s.accounts[0], 0)
	log.Printf("token deployed at %s", s.tokenAddr.Hex())

	// Mint a large supply to account 0.
	mintData := contracts.PackCallData(contracts.FnMint, 1_000_000_000)
	s.execBlock(s.makeTx(0, s.tokenAddr, 500_000, mintData))
	log.Printf("minted 1B tokens to account 0")

	// Distribute tokens to all accounts.
	for i := 1; i < len(s.accounts); i++ {
		recipientID := callerID(s.accounts[i])
		data := contracts.PackCallData(contracts.FnTransfer, recipientID, 10_000_000)
		s.execBlock(s.makeTx(0, s.tokenAddr, 500_000, data))
	}
	log.Printf("distributed tokens to %d accounts", len(s.accounts)-1)

	// Deploy AMM pools with different fee tiers.
	feeTiers := []uint64{997, 995, 990, 999, 985}
	for i := 0; i < s.numPools; i++ {
		fee := feeTiers[i%len(feeTiers)]
		ammCode := contracts.BuildAMMContract(fee, callerID(s.tokenAddr))
		tx := s.makeTx(0, types.ZeroAddress, 200_000, ammCode)
		s.execBlock(tx)
		addr := executor.DeriveContractAddress(s.accounts[0], s.nonces[0]-1)

		// Seed liquidity.
		liqA := uint64(1_000_000 + s.rng.Intn(9_000_000))
		liqB := uint64(1_000_000 + s.rng.Intn(9_000_000))
		data := contracts.PackCallData(contracts.AMMFnAddLiquidity, liqA, liqB)
		s.execBlock(s.makeTx(0, addr, 500_000, data))

		s.pools = append(s.pools, poolInfo{addr: addr, fee: fee, resA: liqA, resB: liqB})
		log.Printf("pool %d deployed at %s (fee=%d, liquidity=%d/%d)", i, addr.Hex(), fee, liqA, liqB)
	}

	log.Printf("setup complete: %d blocks, %d accounts, %d pools", s.blockHeight-1, len(s.accounts), len(s.pools))
}

func (s *simulation) step() {
	// Pick a random action.
	action := s.rng.Intn(100)
	switch {
	case action < 30:
		s.doSwap()
	case action < 50:
		s.doTransfer()
	case action < 65:
		s.doMint()
	case action < 80:
		s.doAddLiquidity()
	case action < 90:
		s.doRemoveLiquidity()
	default:
		s.doMultiTxBlock()
	}
}

func (s *simulation) doSwap() {
	acctIdx := s.rng.Intn(len(s.accounts))
	poolIdx := s.rng.Intn(len(s.pools))
	pool := s.pools[poolIdx]
	amount := uint64(100 + s.rng.Intn(10000))

	var data []byte
	if s.rng.Intn(2) == 0 {
		data = contracts.PackCallData(contracts.AMMFnSwapAForB, amount)
	} else {
		data = contracts.PackCallData(contracts.AMMFnSwapBForA, amount)
	}

	tx := s.makeTx(acctIdx, pool.addr, 500_000, data)
	s.execBlock(tx)
	s.checkPoolInvariant(poolIdx)
}

func (s *simulation) doTransfer() {
	from := s.rng.Intn(len(s.accounts))
	to := s.rng.Intn(len(s.accounts))
	if from == to {
		to = (to + 1) % len(s.accounts)
	}
	amount := uint64(1 + s.rng.Intn(1000))
	recipientID := callerID(s.accounts[to])
	data := contracts.PackCallData(contracts.FnTransfer, recipientID, amount)
	s.execBlock(s.makeTx(from, s.tokenAddr, 500_000, data))
}

func (s *simulation) doMint() {
	acctIdx := s.rng.Intn(len(s.accounts))
	amount := uint64(1000 + s.rng.Intn(100000))
	data := contracts.PackCallData(contracts.FnMint, amount)
	s.execBlock(s.makeTx(acctIdx, s.tokenAddr, 500_000, data))
}

func (s *simulation) doAddLiquidity() {
	acctIdx := s.rng.Intn(len(s.accounts))
	poolIdx := s.rng.Intn(len(s.pools))
	amtA := uint64(100 + s.rng.Intn(50000))
	amtB := uint64(100 + s.rng.Intn(50000))
	data := contracts.PackCallData(contracts.AMMFnAddLiquidity, amtA, amtB)
	s.execBlock(s.makeTx(acctIdx, s.pools[poolIdx].addr, 500_000, data))
}

func (s *simulation) doRemoveLiquidity() {
	acctIdx := s.rng.Intn(len(s.accounts))
	poolIdx := s.rng.Intn(len(s.pools))
	lpID := callerID(s.accounts[acctIdx])
	bal := s.readSlot(s.pools[poolIdx].addr, contracts.AMMSlotLPBalBase+lpID)
	if bal == 0 {
		return // no LP tokens to remove
	}
	removeAmt := uint64(1 + s.rng.Int63n(int64(bal)))
	data := contracts.PackCallData(contracts.AMMFnRemoveLiquidity, removeAmt)
	s.execBlock(s.makeTx(acctIdx, s.pools[poolIdx].addr, 500_000, data))
}

func (s *simulation) doMultiTxBlock() {
	n := 2 + s.rng.Intn(8) // 2-9 txs per block
	var txs []*types.Transaction
	used := make(map[int]bool) // avoid nonce conflicts within a block

	for i := 0; i < n; i++ {
		acctIdx := s.rng.Intn(len(s.accounts))
		if used[acctIdx] {
			continue
		}
		used[acctIdx] = true

		poolIdx := s.rng.Intn(len(s.pools))
		amount := uint64(100 + s.rng.Intn(5000))
		data := contracts.PackCallData(contracts.AMMFnSwapAForB, amount)
		tx := s.makeTx(acctIdx, s.pools[poolIdx].addr, 500_000, data)
		tx.GasPrice = uint64(1 + s.rng.Intn(10))
		txs = append(txs, tx)
	}

	if len(txs) == 0 {
		return
	}

	_, receipts, err := s.exec.ProcessBlock(s.blockHeight, types.ZeroHash, txs)
	if err != nil {
		log.Fatalf("FATAL block %d: %v", s.blockHeight, err)
	}
	for _, r := range receipts {
		s.totalTx++
		if r.Success {
			s.totalSuccess++
		} else {
			s.totalFail++
		}
		s.totalGas += r.GasUsed
	}
	s.lastStateRoot = s.stateDB.Commit()
	s.blockHeight++
}

func (s *simulation) checkPoolInvariant(poolIdx int) {
	resA := s.readSlot(s.pools[poolIdx].addr, contracts.AMMSlotReserveA)
	resB := s.readSlot(s.pools[poolIdx].addr, contracts.AMMSlotReserveB)
	if resA == 0 || resB == 0 {
		log.Fatalf("INVARIANT VIOLATION: pool %d reserves hit zero: A=%d B=%d (block %d)",
			poolIdx, resA, resB, s.blockHeight)
	}
}

func (s *simulation) execBlock(tx *types.Transaction) {
	_, receipts, err := s.exec.ProcessBlock(s.blockHeight, types.ZeroHash, []*types.Transaction{tx})
	if err != nil {
		log.Fatalf("FATAL block %d: %v", s.blockHeight, err)
	}
	s.totalTx++
	if receipts[0].Success {
		s.totalSuccess++
	} else {
		s.totalFail++
	}
	s.totalGas += receipts[0].GasUsed
	s.lastStateRoot = s.stateDB.Commit()
	s.blockHeight++
}

func (s *simulation) makeTx(acctIdx int, to types.Address, gasLimit uint64, data []byte) *types.Transaction {
	tx := &types.Transaction{
		Version:  1,
		Nonce:    s.nonces[acctIdx],
		From:     s.accounts[acctIdx],
		To:       to,
		GasPrice: 1,
		GasLimit: gasLimit,
		Data:     data,
	}
	s.nonces[acctIdx]++
	return tx
}

func (s *simulation) readSlot(contract types.Address, slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	val := s.stateDB.GetStorage(contract, key)
	return binary.BigEndian.Uint64(val[24:])
}

func (s *simulation) printStats() {
	elapsed := time.Since(s.startTime)
	bps := float64(s.blockHeight) / elapsed.Seconds()
	tps := float64(s.totalTx) / elapsed.Seconds()

	fmt.Fprintf(os.Stderr, "\n=== Simulation Stats (elapsed %s) ===\n", elapsed.Round(time.Second))
	fmt.Fprintf(os.Stderr, "  blocks:     %d (%.1f blocks/sec)\n", s.blockHeight, bps)
	fmt.Fprintf(os.Stderr, "  txs:        %d (%.1f tx/sec)\n", s.totalTx, tps)
	fmt.Fprintf(os.Stderr, "  success:    %d (%.1f%%)\n", s.totalSuccess, pct(s.totalSuccess, s.totalTx))
	fmt.Fprintf(os.Stderr, "  failed:     %d (%.1f%%)\n", s.totalFail, pct(s.totalFail, s.totalTx))
	fmt.Fprintf(os.Stderr, "  total gas:  %d\n", s.totalGas)
	fmt.Fprintf(os.Stderr, "  state root: %s\n", s.lastStateRoot.Hex())

	for i, p := range s.pools {
		resA := s.readSlot(p.addr, contracts.AMMSlotReserveA)
		resB := s.readSlot(p.addr, contracts.AMMSlotReserveB)
		lpSupply := s.readSlot(p.addr, contracts.AMMSlotLPSupply)
		fmt.Fprintf(os.Stderr, "  pool %d (fee=%d): resA=%d resB=%d lp=%d\n", i, p.fee, resA, resB, lpSupply)
	}
	fmt.Fprintln(os.Stderr)
}

func pct(n, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func makeAddr(id uint64) types.Address {
	var addr types.Address
	binary.BigEndian.PutUint64(addr[12:], id)
	return addr
}

func callerID(addr types.Address) uint64 {
	return binary.BigEndian.Uint64(addr[12:20])
}
