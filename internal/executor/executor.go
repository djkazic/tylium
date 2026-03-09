package executor

import (
	"fmt"
	"sort"

	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/internal/vm"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/types"
)

const (
	// TxBaseGas is the intrinsic gas cost of any transaction.
	// At gasPrice=1 (1 sat/gas), this targets ~200 sats ($0.20) per simple transfer.
	TxBaseGas = 200
	// CreateBaseGas is additional gas for contract creation.
	CreateBaseGas = 300

	// MaxCodeSize is the maximum contract bytecode size (100KB).
	MaxCodeSize = 100_000
	// MaxCallDataSize is the maximum call data size (64KB).
	MaxCallDataSize = 65_536

	// AMM settlement slot constants (must match contracts.AMMSlot* values).
	ammSlotTokenA       = 3
	ammSlotSettleDebitA  = 300
	ammSlotSettleCreditA = 301
	ammSlotSettleDebitB  = 302
	ammSlotSettleCreditB = 303
	tokenSlotBalanceBase = 1000
)

// Executor processes blocks by executing ordered transactions against state.
type Executor struct {
	stateDB       *state.StateDB
	vm            *vm.VM
	currentHeight uint64
	coinbase      types.Address // miner address; when set, receives gas fees

	// contractByID maps callerID → contract address for deployed contracts.
	// Used by AMM settlement to resolve tokenA address from its callerID.
	contractByID map[uint64]types.Address
}

// New creates an executor with the given state database.
func New(stateDB *state.StateDB) *Executor {
	return &Executor{
		stateDB:      stateDB,
		vm:           &vm.VM{},
		contractByID: make(map[uint64]types.Address),
	}
}

// SetCoinbase sets the miner address that receives gas fees.
// When zero (default), gas fees are burned.
func (e *Executor) SetCoinbase(addr types.Address) {
	e.coinbase = addr
}

// RegisterContract adds a callerID → address mapping to the contract registry.
// Used to restore the registry on node startup from persistent storage.
func (e *Executor) RegisterContract(callerID uint64, addr types.Address) {
	e.contractByID[callerID] = addr
}

// ContractRegistry returns a copy of the current contract registry.
func (e *Executor) ContractRegistry() map[uint64]types.Address {
	m := make(map[uint64]types.Address, len(e.contractByID))
	for k, v := range e.contractByID {
		m[k] = v
	}
	return m
}

// ProcessBlock takes extracted L1 transactions, orders and executes them,
// producing a Block and receipts.
func (e *Executor) ProcessBlock(
	height uint64,
	l1BlockHash types.Hash256,
	txs []*types.Transaction,
) (*types.Block, []*Receipt, error) {

	e.currentHeight = height
	prevRoot := e.stateDB.Root()

	// Deterministic ordering.
	ordered := make([]*types.Transaction, len(txs))
	copy(ordered, txs)
	sort.Slice(ordered, func(i, j int) bool {
		return types.TxLess(ordered[i], ordered[j])
	})

	var receipts []*Receipt
	totalGas := uint64(0)

	var totalFees uint64
	for _, tx := range ordered {
		receipt := e.executeTx(tx)
		receipt.Height = height
		receipts = append(receipts, receipt)
		totalGas += receipt.GasUsed
		totalFees += receipt.GasUsed * tx.GasPrice
	}

	// Credit gas fees to coinbase (miner) if set.
	if !e.coinbase.IsZero() && totalFees > 0 {
		miner := e.stateDB.GetOrCreateAccount(e.coinbase)
		miner.Balance += totalFees
		e.stateDB.SetAccount(e.coinbase, miner)
	}

	stateRoot := e.stateDB.Commit()
	txRoot := computeTxRoot(ordered)
	receiptRoot := computeReceiptRoot(receipts)

	block := &types.Block{
		Height:        height,
		L1BlockHash:   l1BlockHash,
		PrevStateRoot: prevRoot,
		StateRoot:     stateRoot,
		TxRoot:        txRoot,
		ReceiptRoot:   receiptRoot,
		Transactions:  ordered,
		GasUsed:       totalGas,
		Coinbase:      e.coinbase,
	}

	return block, receipts, nil
}

func (e *Executor) executeTx(tx *types.Transaction) *Receipt {
	txid := tx.TxID()

	// System deposit: From == zero address means this is an L1 deposit.
	// Credit the recipient directly — no gas, no nonce check.
	if tx.From.IsZero() {
		if tx.Value > 0 {
			recipient := e.stateDB.GetOrCreateAccount(tx.To)
			recipient.Balance += tx.Value
			e.stateDB.SetAccount(tx.To, recipient)
		}
		return &Receipt{TxID: txid, Success: true, GasUsed: 0}
	}

	// Verify signature: recover signer and check it matches tx.From.
	sigHash := tx.SigningHash()
	if !crypto.VerifySender(sigHash, tx.Signature, tx.From) {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "invalid signature"}
	}

	// Size limits.
	if tx.To.IsZero() && len(tx.Data) > MaxCodeSize {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "contract code exceeds max size"}
	}
	if !tx.To.IsZero() && len(tx.Data) > MaxCallDataSize {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "call data exceeds max size"}
	}

	// Validate basic gas requirement.
	intrinsicGas := uint64(TxBaseGas)
	if tx.To.IsZero() {
		intrinsicGas += CreateBaseGas
	}
	if tx.GasLimit < intrinsicGas {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "gas limit below intrinsic gas"}
	}

	// Check sender has sufficient balance for value + gas (with overflow protection).
	sender := e.stateDB.GetOrCreateAccount(tx.From)
	gasCost := tx.GasPrice * tx.GasLimit
	if tx.GasPrice != 0 && gasCost/tx.GasPrice != tx.GasLimit {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "gas cost overflow"}
	}
	maxCost := tx.Value + gasCost
	if maxCost < tx.Value {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "value + gas overflow"}
	}
	if sender.Balance < maxCost {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: "insufficient balance"}
	}

	// Check nonce.
	if tx.Nonce != sender.Nonce {
		return &Receipt{TxID: txid, Success: false, GasUsed: 0, Err: fmt.Sprintf("nonce mismatch: expected %d, got %d", sender.Nonce, tx.Nonce)}
	}

	// Take snapshot for revert on failure.
	snap := e.stateDB.Snapshot()

	// Deduct gas upfront.
	sender.Nonce++
	sender.Balance -= maxCost
	e.stateDB.SetAccount(tx.From, sender)

	// Transfer value.
	if tx.Value > 0 {
		recipient := e.stateDB.GetOrCreateAccount(tx.To)
		recipient.Balance += tx.Value
		e.stateDB.SetAccount(tx.To, recipient)
	}

	gasLeft := tx.GasLimit - intrinsicGas
	gasUsed := intrinsicGas

	// Execute contract code if applicable.
	if tx.To == HTLCSystemAddress && len(tx.Data) > 0 {
		// HTLC precompile — native handler, no VM.
		e.writeCallData(tx.To, tx.From, tx.Data)
		handler := &htlcHandler{stateDB: e.stateDB, currentHeight: e.currentHeight}
		ok, errMsg := handler.processHTLC(tx)
		if !ok {
			e.stateDB.RevertToSnapshot(snap)
			sender = e.stateDB.GetOrCreateAccount(tx.From)
			sender.Nonce++
			sender.Balance -= tx.GasPrice * gasUsed
			e.stateDB.SetAccount(tx.From, sender)
			return &Receipt{TxID: txid, Success: false, GasUsed: gasUsed, Err: errMsg}
		}
	} else if tx.To.IsZero() {
		// Contract creation.
		contractAddr := deriveContractAddress(tx.From, tx.Nonce)
		e.stateDB.SetCode(contractAddr, tx.Data)
		e.contractByID[contractAddr.CallerID()] = contractAddr
		gasUsed = intrinsicGas // creation just stores code for now
	} else if len(tx.Data) > 0 {
		// Contract call.
		acct := e.stateDB.GetAccount(tx.To)
		if acct != nil && acct.IsContract() {
			code := e.stateDB.GetCode(acct.CodeHash)
			if code != nil {
				// Unpack call data into contract storage slots 100+.
				// Format: each 8 bytes is a uint64 value for slots 100, 101, 102, ...
				// Slot 103 is always set to the caller identifier (low 8 bytes of address).
				e.writeCallData(tx.To, tx.From, tx.Data)

				ctx := &vm.Context{
					Caller:  tx.From,
					Target:  tx.To,
					Value:   tx.Value,
					GasLeft: gasLeft,
					Code:    code,
					State:   e.stateDB,
				}
				result := e.vm.Execute(ctx)
				gasUsed += result.GasUsed
				if !result.Success {
					e.stateDB.RevertToSnapshot(snap)
					// Sender still pays gas.
					sender = e.stateDB.GetOrCreateAccount(tx.From)
					sender.Nonce++
					sender.Balance -= tx.GasPrice * gasUsed
					e.stateDB.SetAccount(tx.From, sender)
					return &Receipt{TxID: txid, Success: false, GasUsed: gasUsed, Err: result.Err.Error()}
				}

				// AMM settlement: if the contract wrote settlement slots, perform
				// actual token and tyBTC transfers.
				if err := e.settleAMM(tx.To, tx.From); err != nil {
					e.stateDB.RevertToSnapshot(snap)
					sender = e.stateDB.GetOrCreateAccount(tx.From)
					sender.Nonce++
					sender.Balance -= tx.GasPrice * gasUsed
					e.stateDB.SetAccount(tx.From, sender)
					return &Receipt{TxID: txid, Success: false, GasUsed: gasUsed, Err: err.Error()}
				}
			}
		}
	}

	// Refund unused gas.
	refund := (tx.GasLimit - gasUsed) * tx.GasPrice
	sender = e.stateDB.GetOrCreateAccount(tx.From)
	sender.Balance += refund
	e.stateDB.SetAccount(tx.From, sender)

	return &Receipt{TxID: txid, Success: true, GasUsed: gasUsed}
}

// writeCallData unpacks tx.Data into contract storage slots for the VM.
// Data format: packed uint64s (big-endian, 8 bytes each) written to slots 100, 101, 102, ...
// Slot 103 is reserved for the caller identifier (derived from the sender address).
func (e *Executor) writeCallData(contract types.Address, caller types.Address, data []byte) {
	// Write packed uint64 args to slots 100, 101, 102, ...
	slot := uint64(100)
	for i := 0; i+8 <= len(data); i += 8 {
		val := uint64(0)
		for j := 0; j < 8; j++ {
			val = (val << 8) | uint64(data[i+j])
		}
		var key, v types.Hash256
		putUint64(&key, slot)
		putUint64(&v, val)
		e.stateDB.SetStorage(contract, key, v)
		slot++
		// Skip slot 103 for args — it's reserved for caller.
		if slot == 103 {
			slot = 104
		}
	}

	// Set caller identifier in slot 103 (low 8 bytes of address).
	var callerKey, callerVal types.Hash256
	putUint64(&callerKey, 103)
	callerID := uint64(0)
	for i := 12; i < 20; i++ {
		callerID = (callerID << 8) | uint64(caller[i])
	}
	putUint64(&callerVal, callerID)
	e.stateDB.SetStorage(contract, callerKey, callerVal)
}

func putUint64(h *types.Hash256, v uint64) {
	h[24] = byte(v >> 56)
	h[25] = byte(v >> 48)
	h[26] = byte(v >> 40)
	h[27] = byte(v >> 32)
	h[28] = byte(v >> 24)
	h[29] = byte(v >> 16)
	h[30] = byte(v >> 8)
	h[31] = byte(v)
}

// DeriveContractAddress computes the address for a contract created by the
// given creator at the given nonce.
func DeriveContractAddress(creator types.Address, nonce uint64) types.Address {
	return deriveContractAddress(creator, nonce)
}

func deriveContractAddress(creator types.Address, nonce uint64) types.Address {
	var buf []byte
	buf = append(buf, creator[:]...)
	buf = append(buf, byte(nonce>>56), byte(nonce>>48), byte(nonce>>40), byte(nonce>>32))
	buf = append(buf, byte(nonce>>24), byte(nonce>>16), byte(nonce>>8), byte(nonce))
	hash := types.Sha256(buf)
	var addr types.Address
	copy(addr[:], hash[:20])
	return addr
}

func computeTxRoot(txs []*types.Transaction) types.Hash256 {
	if len(txs) == 0 {
		return types.ZeroHash
	}
	var data []byte
	for _, tx := range txs {
		id := tx.TxID()
		data = append(data, id[:]...)
	}
	return types.Sha256(data)
}

func computeReceiptRoot(receipts []*Receipt) types.Hash256 {
	if len(receipts) == 0 {
		return types.ZeroHash
	}
	var data []byte
	for _, r := range receipts {
		data = append(data, r.TxID[:]...)
		if r.Success {
			data = append(data, 1)
		} else {
			data = append(data, 0)
		}
	}
	return types.Sha256(data)
}

// --- AMM Settlement ---

// settleAMM checks if a contract wrote AMM settlement slots (300-303) and
// performs actual token and tyBTC transfers. Returns nil if no settlement
// needed, or an error if settlement fails (e.g., insufficient balance).
func (e *Executor) settleAMM(contract, caller types.Address) error {
	tokenAID := e.readStorageUint64(contract, ammSlotTokenA)
	if tokenAID == 0 {
		return nil // not an AMM or no token pair configured
	}

	debitA := e.readStorageUint64(contract, ammSlotSettleDebitA)
	creditA := e.readStorageUint64(contract, ammSlotSettleCreditA)
	debitB := e.readStorageUint64(contract, ammSlotSettleDebitB)
	creditB := e.readStorageUint64(contract, ammSlotSettleCreditB)

	if debitA == 0 && creditA == 0 && debitB == 0 && creditB == 0 {
		return nil
	}

	tokenAddr, ok := e.contractByID[tokenAID]
	if !ok {
		return fmt.Errorf("AMM settlement: tokenA contract not found (callerID=%d)", tokenAID)
	}

	callerID := caller.CallerID()
	ammID := contract.CallerID()

	// TokenA transfers (in the token contract's storage).
	if debitA > 0 {
		if err := e.transferToken(tokenAddr, callerID, ammID, debitA); err != nil {
			return fmt.Errorf("AMM settlement debitA: %w", err)
		}
	}
	if creditA > 0 {
		if err := e.transferToken(tokenAddr, ammID, callerID, creditA); err != nil {
			return fmt.Errorf("AMM settlement creditA: %w", err)
		}
	}

	// tyBTC transfers (native balance).
	if debitB > 0 {
		callerAcct := e.stateDB.GetOrCreateAccount(caller)
		if callerAcct.Balance < debitB {
			return fmt.Errorf("AMM settlement: insufficient tyBTC (have %d, need %d)", callerAcct.Balance, debitB)
		}
		callerAcct.Balance -= debitB
		e.stateDB.SetAccount(caller, callerAcct)

		ammAcct := e.stateDB.GetOrCreateAccount(contract)
		ammAcct.Balance += debitB
		e.stateDB.SetAccount(contract, ammAcct)
	}
	if creditB > 0 {
		ammAcct := e.stateDB.GetOrCreateAccount(contract)
		if ammAcct.Balance < creditB {
			return fmt.Errorf("AMM settlement: insufficient pool tyBTC (have %d, need %d)", ammAcct.Balance, creditB)
		}
		ammAcct.Balance -= creditB
		e.stateDB.SetAccount(contract, ammAcct)

		callerAcct := e.stateDB.GetOrCreateAccount(caller)
		callerAcct.Balance += creditB
		e.stateDB.SetAccount(caller, callerAcct)
	}

	// Clear settlement slots to avoid stale data.
	e.writeStorageUint64(contract, ammSlotSettleDebitA, 0)
	e.writeStorageUint64(contract, ammSlotSettleCreditA, 0)
	e.writeStorageUint64(contract, ammSlotSettleDebitB, 0)
	e.writeStorageUint64(contract, ammSlotSettleCreditB, 0)

	return nil
}

// transferToken moves amount of a token from one callerID to another in the
// token contract's storage.
func (e *Executor) transferToken(tokenAddr types.Address, fromID, toID, amount uint64) error {
	fromBal := e.readStorageUint64(tokenAddr, tokenSlotBalanceBase+fromID)
	if fromBal < amount {
		return fmt.Errorf("insufficient token balance (have %d, need %d)", fromBal, amount)
	}
	toBal := e.readStorageUint64(tokenAddr, tokenSlotBalanceBase+toID)

	e.writeStorageUint64(tokenAddr, tokenSlotBalanceBase+fromID, fromBal-amount)
	e.writeStorageUint64(tokenAddr, tokenSlotBalanceBase+toID, toBal+amount)
	return nil
}

func (e *Executor) readStorageUint64(addr types.Address, slot uint64) uint64 {
	var key types.Hash256
	putUint64(&key, slot)
	val := e.stateDB.GetStorage(addr, key)
	return getUint64(val)
}

func (e *Executor) writeStorageUint64(addr types.Address, slot, val uint64) {
	var key, v types.Hash256
	putUint64(&key, slot)
	putUint64(&v, val)
	e.stateDB.SetStorage(addr, key, v)
}

func getUint64(h types.Hash256) uint64 {
	return uint64(h[24])<<56 | uint64(h[25])<<48 | uint64(h[26])<<40 | uint64(h[27])<<32 |
		uint64(h[28])<<24 | uint64(h[29])<<16 | uint64(h[30])<<8 | uint64(h[31])
}
