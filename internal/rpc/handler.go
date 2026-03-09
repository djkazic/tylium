package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/internal/store"
	"github.com/djkazic/tylium/pkg/types"
)

// NodeInfo provides read access to current node state.
type NodeInfo interface {
	Height() uint64
	StateRoot() types.Hash256
	FinalizedHeight() uint64
	FinalizedRoot() types.Hash256
}

// Handler dispatches JSON-RPC method calls.
type Handler struct {
	nodeInfo NodeInfo
	stateDB  *state.StateDB
	store    *store.Store
}

// NewHandler creates an RPC handler.
func NewHandler(info NodeInfo, stateDB *state.StateDB, st *store.Store) *Handler {
	return &Handler{
		nodeInfo: info,
		stateDB:  stateDB,
		store:    st,
	}
}

// Handle routes a method call to the appropriate handler.
func (h *Handler) Handle(method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tyl_blockHeight":
		return h.blockHeight()
	case "tyl_stateRoot":
		return h.stateRoot()
	case "tyl_finalizedHeight":
		return h.finalizedHeight()
	case "tyl_getAccount":
		return h.getAccount(params)
	case "tyl_getBalance":
		return h.getBalance(params)
	case "tyl_getStorage":
		return h.getStorage(params)
	case "tyl_getCode":
		return h.getCode(params)
	case "tyl_getBlock":
		return h.getBlock(params)
	case "tyl_getReceipts":
		return h.getReceipts(params)
	case "tyl_getReceipt":
		return h.getReceipt(params)
	case "tyl_recentTxs":
		return h.recentTxs(params)
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

// --- Response types ---

type BlockHeightResult struct {
	Height uint64 `json:"height"`
}

type StateRootResult struct {
	StateRoot string `json:"stateRoot"`
}

type AccountResult struct {
	Address     string `json:"address"`
	Nonce       uint64 `json:"nonce"`
	Balance     uint64 `json:"balance"`
	CodeHash    string `json:"codeHash"`
	StorageRoot string `json:"storageRoot"`
	IsContract  bool   `json:"isContract"`
}

type BalanceResult struct {
	Address           string `json:"address"`
	Balance           uint64 `json:"balance"`
	FinalizedBalance  *uint64 `json:"finalizedBalance,omitempty"`
}

type StorageResult struct {
	Address        string  `json:"address"`
	Key            string  `json:"key"`
	Value          string  `json:"value"`
	FinalizedValue *string `json:"finalizedValue,omitempty"`
}

type CodeResult struct {
	Address  string `json:"address"`
	Code     string `json:"code"`
	CodeHash string `json:"codeHash"`
}

// --- Handlers ---

func (h *Handler) blockHeight() (*BlockHeightResult, error) {
	return &BlockHeightResult{Height: h.nodeInfo.Height()}, nil
}

type FinalizedResult struct {
	FinalizedHeight uint64 `json:"finalizedHeight"`
	FinalizedRoot   string `json:"finalizedRoot"`
	OptimisticHeight uint64 `json:"optimisticHeight"`
}

func (h *Handler) finalizedHeight() (*FinalizedResult, error) {
	return &FinalizedResult{
		FinalizedHeight:  h.nodeInfo.FinalizedHeight(),
		FinalizedRoot:    h.nodeInfo.FinalizedRoot().Hex(),
		OptimisticHeight: h.nodeInfo.Height(),
	}, nil
}

func (h *Handler) stateRoot() (*StateRootResult, error) {
	return &StateRootResult{StateRoot: h.nodeInfo.StateRoot().Hex()}, nil
}

func (h *Handler) getAccount(params json.RawMessage) (*AccountResult, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
		return nil, fmt.Errorf("expected [address]")
	}

	addr, err := parseAddress(args[0])
	if err != nil {
		return nil, err
	}

	acct := h.stateDB.GetAccount(addr)
	if acct == nil {
		return &AccountResult{Address: addr.Hex()}, nil
	}

	return &AccountResult{
		Address:     addr.Hex(),
		Nonce:       acct.Nonce,
		Balance:     acct.Balance,
		CodeHash:    acct.CodeHash.Hex(),
		StorageRoot: acct.StorageRoot.Hex(),
		IsContract:  acct.IsContract(),
	}, nil
}

func (h *Handler) getBalance(params json.RawMessage) (*BalanceResult, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
		return nil, fmt.Errorf("expected [address]")
	}

	addr, err := parseAddress(args[0])
	if err != nil {
		return nil, err
	}

	balance := h.stateDB.GetBalance(addr)
	result := &BalanceResult{
		Address: addr.Hex(),
		Balance: balance,
	}
	if fb, ok := h.stateDB.GetFinalizedBalance(addr); ok {
		result.FinalizedBalance = &fb
	}
	return result, nil
}

func (h *Handler) getStorage(params json.RawMessage) (*StorageResult, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 2 {
		return nil, fmt.Errorf("expected [address, key]")
	}

	addr, err := parseAddress(args[0])
	if err != nil {
		return nil, err
	}

	keyBytes, err := hex.DecodeString(stripHexPrefix(args[1]))
	if err != nil {
		return nil, fmt.Errorf("invalid key hex: %w", err)
	}
	key := types.BytesToHash256(keyBytes)

	val := h.stateDB.GetStorage(addr, key)
	result := &StorageResult{
		Address: addr.Hex(),
		Key:     key.Hex(),
		Value:   val.Hex(),
	}
	if fv, ok := h.stateDB.GetFinalizedStorage(addr, key); ok {
		hex := fv.Hex()
		result.FinalizedValue = &hex
	}
	return result, nil
}

func (h *Handler) getCode(params json.RawMessage) (*CodeResult, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
		return nil, fmt.Errorf("expected [address]")
	}

	addr, err := parseAddress(args[0])
	if err != nil {
		return nil, err
	}

	acct := h.stateDB.GetAccount(addr)
	if acct == nil || !acct.IsContract() {
		return &CodeResult{Address: addr.Hex()}, nil
	}

	code := h.stateDB.GetCode(acct.CodeHash)
	return &CodeResult{
		Address:  addr.Hex(),
		Code:     hex.EncodeToString(code),
		CodeHash: acct.CodeHash.Hex(),
	}, nil
}

func (h *Handler) getBlock(params json.RawMessage) (json.RawMessage, error) {
	var args []uint64
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
		return nil, fmt.Errorf("expected [height]")
	}

	data, err := h.store.GetBlock(args[0])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return json.RawMessage("null"), nil
	}
	return data, nil
}

func (h *Handler) getReceipts(params json.RawMessage) (json.RawMessage, error) {
	var args []uint64
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
		return nil, fmt.Errorf("expected [height]")
	}

	data, err := h.store.GetReceipts(args[0])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return json.RawMessage("[]"), nil
	}
	return data, nil
}

func (h *Handler) getReceipt(params json.RawMessage) (json.RawMessage, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
		return nil, fmt.Errorf("expected [txid]")
	}

	txidBytes, err := hex.DecodeString(stripHexPrefix(args[0]))
	if err != nil || len(txidBytes) != 32 {
		return nil, fmt.Errorf("invalid txid: must be 32-byte hex")
	}
	txid := types.BytesToHash256(txidBytes)

	data, err := h.store.GetTxReceipt(txid)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return json.RawMessage("null"), nil
	}

	// Enrich with finalization status.
	r := unmarshalReceipt(data)
	finalized := h.nodeInfo.FinalizedHeight() >= r.Height && r.Height > 0
	enriched := struct {
		TxID      string `json:"txid"`
		Height    uint64 `json:"height"`
		Success   bool   `json:"success"`
		GasUsed   uint64 `json:"gasUsed"`
		Err       string `json:"err,omitempty"`
		Finalized bool   `json:"finalized"`
	}{
		TxID:      hex.EncodeToString(r.TxID),
		Height:    r.Height,
		Success:   r.Success,
		GasUsed:   r.GasUsed,
		Err:       r.Err,
		Finalized: finalized,
	}
	result, _ := json.Marshal(enriched)
	return result, nil
}

func (h *Handler) recentTxs(params json.RawMessage) (json.RawMessage, error) {
	offset := 0
	limit := 20
	if params != nil {
		var args []int
		if err := json.Unmarshal(params, &args); err == nil {
			if len(args) >= 1 && args[0] >= 0 {
				offset = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				limit = args[1]
			}
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	txids, err := h.store.ListRecentTxs(offset, limit)
	if err != nil {
		return nil, err
	}

	type txEntry struct {
		TxID      string `json:"txid"`
		Height    uint64 `json:"height"`
		Success   bool   `json:"success"`
		GasUsed   uint64 `json:"gasUsed"`
		Err       string `json:"err,omitempty"`
		Finalized bool   `json:"finalized"`
	}

	entries := make([]txEntry, 0, len(txids))
	finalizedHeight := h.nodeInfo.FinalizedHeight()
	for _, txid := range txids {
		data, err := h.store.GetTxReceipt(txid)
		if err != nil || data == nil {
			continue
		}
		r := unmarshalReceipt(data)
		entries = append(entries, txEntry{
			TxID:      hex.EncodeToString(r.TxID),
			Height:    r.Height,
			Success:   r.Success,
			GasUsed:   r.GasUsed,
			Err:       r.Err,
			Finalized: finalizedHeight >= r.Height && r.Height > 0,
		})
	}

	result, _ := json.Marshal(entries)
	return result, nil
}

// storedReceipt matches the JSON shape of executor.Receipt as stored in LevelDB.
// TxID is [32]byte which Go JSON-marshals as base64.
type storedReceipt struct {
	TxID    []byte `json:"TxID"`
	Height  uint64 `json:"Height"`
	Success bool   `json:"Success"`
	GasUsed uint64 `json:"GasUsed"`
	Err     string `json:"Err"`
}

func unmarshalReceipt(data []byte) storedReceipt {
	var r storedReceipt
	json.Unmarshal(data, &r)
	return r
}

func parseAddress(s string) (types.Address, error) {
	s = stripHexPrefix(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return types.ZeroAddress, fmt.Errorf("invalid address hex: %w", err)
	}
	if len(b) != 20 {
		return types.ZeroAddress, fmt.Errorf("address must be 20 bytes, got %d", len(b))
	}
	return types.BytesToAddress(b), nil
}

func stripHexPrefix(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}
