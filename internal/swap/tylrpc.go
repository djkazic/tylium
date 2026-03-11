package swap

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/pkg/types"
)

// TyliumRPC is a lightweight client for the Tylium node JSON-RPC.
type TyliumRPC struct {
	endpoint string
}

func NewTyliumRPC(endpoint string) *TyliumRPC {
	return &TyliumRPC{endpoint: endpoint}
}

// GetBlockHeight returns the current optimistic block height.
func (c *TyliumRPC) GetBlockHeight() (uint64, error) {
	var result struct {
		Height uint64 `json:"height"`
	}
	if err := c.call("tyl_blockHeight", []interface{}{}, &result); err != nil {
		return 0, err
	}
	return result.Height, nil
}

// GetAccount returns account nonce and balance.
func (c *TyliumRPC) GetAccount(addr types.Address) (nonce, balance uint64, err error) {
	var result struct {
		Nonce   uint64 `json:"nonce"`
		Balance uint64 `json:"balance"`
	}
	if err := c.call("tyl_getAccount", []string{addr.Hex()}, &result); err != nil {
		return 0, 0, err
	}
	return result.Nonce, result.Balance, nil
}

// storageResult holds the response from tyl_getStorage.
type storageResult struct {
	Value          string  `json:"value"`
	FinalizedValue *string `json:"finalizedValue,omitempty"`
}

// GetStorage reads a single storage slot from a contract.
func (c *TyliumRPC) GetStorage(addr types.Address, slot uint64) (uint64, error) {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)

	var result storageResult
	if err := c.call("tyl_getStorage", []string{addr.Hex(), key.Hex()}, &result); err != nil {
		return 0, err
	}

	valBytes, err := hex.DecodeString(result.Value)
	if err != nil || len(valBytes) != 32 {
		return 0, fmt.Errorf("invalid storage value")
	}
	return binary.BigEndian.Uint64(valBytes[24:]), nil
}

// GetFinalizedStorage reads a storage slot's finalized value.
// Returns (value, true) if finalized, (0, false) if no finalized snapshot.
func (c *TyliumRPC) GetFinalizedStorage(addr types.Address, slot uint64) (uint64, bool, error) {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)

	var result storageResult
	if err := c.call("tyl_getStorage", []string{addr.Hex(), key.Hex()}, &result); err != nil {
		return 0, false, err
	}

	if result.FinalizedValue == nil {
		return 0, false, nil
	}

	valBytes, err := hex.DecodeString(*result.FinalizedValue)
	if err != nil || len(valBytes) != 32 {
		return 0, false, fmt.Errorf("invalid finalized storage value")
	}
	return binary.BigEndian.Uint64(valBytes[24:]), true, nil
}

// HTLCInfo holds the on-chain state of an HTLC.
type HTLCInfo struct {
	SenderID    uint64
	RecipientID uint64
	Amount      uint64
	Hashlock    [32]byte
	Timelock    uint64
	Status      uint64 // 0=pending, 1=claimed, 2=refunded
	Preimage    [32]byte
}

// GetHTLC reads full HTLC state from storage.
func (c *TyliumRPC) GetHTLC(htlcID uint64) (*HTLCInfo, error) {
	addr := executor.HTLCSystemAddress
	base := uint64(10000) + htlcID*14

	// Read all slots.
	senderID, err := c.GetStorage(addr, base+0)
	if err != nil {
		return nil, err
	}
	recipientID, _ := c.GetStorage(addr, base+1)
	amount, _ := c.GetStorage(addr, base+2)
	hw0, _ := c.GetStorage(addr, base+3)
	hw1, _ := c.GetStorage(addr, base+4)
	hw2, _ := c.GetStorage(addr, base+5)
	hw3, _ := c.GetStorage(addr, base+6)
	timelock, _ := c.GetStorage(addr, base+7)
	status, _ := c.GetStorage(addr, base+8)
	pw0, _ := c.GetStorage(addr, base+9)
	pw1, _ := c.GetStorage(addr, base+10)
	pw2, _ := c.GetStorage(addr, base+11)
	pw3, _ := c.GetStorage(addr, base+12)

	var hashlock [32]byte
	binary.BigEndian.PutUint64(hashlock[0:8], hw0)
	binary.BigEndian.PutUint64(hashlock[8:16], hw1)
	binary.BigEndian.PutUint64(hashlock[16:24], hw2)
	binary.BigEndian.PutUint64(hashlock[24:32], hw3)

	var preimage [32]byte
	binary.BigEndian.PutUint64(preimage[0:8], pw0)
	binary.BigEndian.PutUint64(preimage[8:16], pw1)
	binary.BigEndian.PutUint64(preimage[16:24], pw2)
	binary.BigEndian.PutUint64(preimage[24:32], pw3)

	return &HTLCInfo{
		SenderID:    senderID,
		RecipientID: recipientID,
		Amount:      amount,
		Hashlock:    hashlock,
		Timelock:    timelock,
		Status:      status,
		Preimage:    preimage,
	}, nil
}

// GetFinalizedHTLC reads HTLC state from finalized storage only.
// Returns (info, true) if finalized snapshot exists, (nil, false) otherwise.
func (c *TyliumRPC) GetFinalizedHTLC(htlcID uint64) (*HTLCInfo, bool, error) {
	addr := executor.HTLCSystemAddress
	base := uint64(10000) + htlcID*14

	status, ok, err := c.GetFinalizedStorage(addr, base+8)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}

	senderID, _, _ := c.GetFinalizedStorage(addr, base+0)
	recipientID, _, _ := c.GetFinalizedStorage(addr, base+1)
	amount, _, _ := c.GetFinalizedStorage(addr, base+2)
	hw0, _, _ := c.GetFinalizedStorage(addr, base+3)
	hw1, _, _ := c.GetFinalizedStorage(addr, base+4)
	hw2, _, _ := c.GetFinalizedStorage(addr, base+5)
	hw3, _, _ := c.GetFinalizedStorage(addr, base+6)
	timelock, _, _ := c.GetFinalizedStorage(addr, base+7)
	pw0, _, _ := c.GetFinalizedStorage(addr, base+9)
	pw1, _, _ := c.GetFinalizedStorage(addr, base+10)
	pw2, _, _ := c.GetFinalizedStorage(addr, base+11)
	pw3, _, _ := c.GetFinalizedStorage(addr, base+12)

	var hashlock [32]byte
	binary.BigEndian.PutUint64(hashlock[0:8], hw0)
	binary.BigEndian.PutUint64(hashlock[8:16], hw1)
	binary.BigEndian.PutUint64(hashlock[16:24], hw2)
	binary.BigEndian.PutUint64(hashlock[24:32], hw3)

	var preimage [32]byte
	binary.BigEndian.PutUint64(preimage[0:8], pw0)
	binary.BigEndian.PutUint64(preimage[8:16], pw1)
	binary.BigEndian.PutUint64(preimage[16:24], pw2)
	binary.BigEndian.PutUint64(preimage[24:32], pw3)

	return &HTLCInfo{
		SenderID:    senderID,
		RecipientID: recipientID,
		Amount:      amount,
		Hashlock:    hashlock,
		Timelock:    timelock,
		Status:      status,
		Preimage:    preimage,
	}, true, nil
}

// GetNextHTLCID reads the HTLC counter to know the next ID that will be assigned.
func (c *TyliumRPC) GetNextHTLCID() (uint64, error) {
	return c.GetStorage(executor.HTLCSystemAddress, 0)
}

func (c *TyliumRPC) call(method string, params interface{}, out interface{}) error {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})

	resp, err := http.Post(c.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error: %s", rpcResp.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(rpcResp.Result, out)
	}
	return nil
}
