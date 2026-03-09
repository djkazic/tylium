package bitcoin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/djkazic/tylium/pkg/types"
)

// Client is the interface for interacting with a Bitcoin node.
type Client interface {
	GetBlockByHeight(height uint64) (*Block, error)
	GetBestBlockHeight() (uint64, error)
	SendRawTransaction(raw []byte) (types.Hash256, error)
	SubscribeNewBlocks(ctx context.Context, fromHeight uint64) (<-chan *Block, error)
}

// Block is a minimal representation of a Bitcoin block.
type Block struct {
	Height   uint64
	Hash     types.Hash256
	PrevHash types.Hash256 // previous block hash (for reorg detection)
	Txs      []Tx
}

// Tx is a minimal Bitcoin transaction.
type Tx struct {
	TxID    types.Hash256
	Inputs  []TxInput
	Outputs []TxOutput
}

// TxInput is a Bitcoin transaction input with witness data.
type TxInput struct {
	Witness [][]byte // segregated witness stack items
}

// TxOutput is a Bitcoin transaction output.
type TxOutput struct {
	Script []byte // raw scriptPubKey
	Value  uint64 // satoshis
}

// RPCClient implements Client via Bitcoin Core JSON-RPC.
type RPCClient struct {
	endpoint  string
	walletURL string // endpoint + /wallet/<name>, set by SetWallet
	user      string
	pass      string
	http      *http.Client
}

// NewRPCClient creates a new Bitcoin Core RPC client.
func NewRPCClient(endpoint, user, pass string) *RPCClient {
	return &RPCClient{
		endpoint: endpoint,
		user:     user,
		pass:     pass,
		http:     &http.Client{},
	}
}

// SetWallet configures the client to use a specific Bitcoin Core wallet.
// Wallet-specific calls (signing, funding) use /wallet/<name> URI path.
func (c *RPCClient) SetWallet(name string) {
	c.walletURL = c.endpoint + "/wallet/" + name
}

// EnsureWallet creates or loads a wallet by name, then sets it as active.
func (c *RPCClient) EnsureWallet(name string) {
	// Try to create; ignore errors (already exists is fine).
	c.call("createwallet", name)
	// Try to load; ignore errors (already loaded is fine).
	c.call("loadwallet", name)
	c.SetWallet(name)
}

// walletEndpoint returns the wallet URL if set, otherwise the base endpoint.
func (c *RPCClient) walletEndpoint() string {
	if c.walletURL != "" {
		return c.walletURL
	}
	return c.endpoint
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *RPCClient) call(method string, params ...interface{}) (json.RawMessage, error) {
	return c.callEndpoint(c.endpoint, method, params...)
}

func (c *RPCClient) callEndpoint(endpoint, method string, params ...interface{}) (json.RawMessage, error) {
	req := rpcRequest{
		JSONRPC: "1.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(c.user, c.pass)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rpc call %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("rpc auth %s: HTTP %d (check rpcuser/rpcpass)", method, resp.StatusCode)
	}

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("rpc decode %s (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %s: %s", method, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

func (c *RPCClient) GetBestBlockHeight() (uint64, error) {
	result, err := c.call("getblockcount")
	if err != nil {
		return 0, err
	}
	var height uint64
	if err := json.Unmarshal(result, &height); err != nil {
		return 0, err
	}
	return height, nil
}

func (c *RPCClient) GetBlockByHeight(height uint64) (*Block, error) {
	// Get block hash at height.
	hashResult, err := c.call("getblockhash", height)
	if err != nil {
		return nil, err
	}
	var blockHash string
	if err := json.Unmarshal(hashResult, &blockHash); err != nil {
		return nil, err
	}

	// Get block with verbosity=2 (full tx details).
	blockResult, err := c.call("getblock", blockHash, 2)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Hash              string `json:"hash"`
		PreviousBlockHash string `json:"previousblockhash"`
		Height            uint64 `json:"height"`
		Tx                []struct {
			TxID string `json:"txid"`
			Vin  []struct {
				TxinWitness []string `json:"txinwitness"`
			} `json:"vin"`
			Vout []struct {
				Value        float64 `json:"value"`
				ScriptPubKey struct {
					Hex string `json:"hex"`
				} `json:"scriptPubKey"`
			} `json:"vout"`
		} `json:"tx"`
	}
	if err := json.Unmarshal(blockResult, &raw); err != nil {
		return nil, err
	}

	block := &Block{
		Height: raw.Height,
	}

	// Parse block hash.
	hashBytes, _ := hexToBytes(raw.Hash)
	block.Hash = types.BytesToHash256(hashBytes)

	// Parse previous block hash (empty for genesis block).
	if raw.PreviousBlockHash != "" {
		prevBytes, _ := hexToBytes(raw.PreviousBlockHash)
		block.PrevHash = types.BytesToHash256(prevBytes)
	}

	// Parse transactions.
	for _, rawTx := range raw.Tx {
		tx := Tx{}
		txidBytes, _ := hexToBytes(rawTx.TxID)
		tx.TxID = types.BytesToHash256(txidBytes)

		for _, vin := range rawTx.Vin {
			input := TxInput{}
			for _, witnessHex := range vin.TxinWitness {
				witnessBytes, _ := hexToBytes(witnessHex)
				input.Witness = append(input.Witness, witnessBytes)
			}
			tx.Inputs = append(tx.Inputs, input)
		}

		for _, vout := range rawTx.Vout {
			scriptBytes, _ := hexToBytes(vout.ScriptPubKey.Hex)
			tx.Outputs = append(tx.Outputs, TxOutput{
				Script: scriptBytes,
				Value:  uint64(vout.Value * 1e8),
			})
		}
		block.Txs = append(block.Txs, tx)
	}

	return block, nil
}

func (c *RPCClient) SendRawTransaction(raw []byte) (types.Hash256, error) {
	hexStr := bytesToHex(raw)
	result, err := c.call("sendrawtransaction", hexStr)
	if err != nil {
		return types.ZeroHash, err
	}
	var txid string
	if err := json.Unmarshal(result, &txid); err != nil {
		return types.ZeroHash, err
	}
	txidBytes, _ := hexToBytes(txid)
	return types.BytesToHash256(txidBytes), nil
}

func (c *RPCClient) SubscribeNewBlocks(ctx context.Context, fromHeight uint64) (<-chan *Block, error) {
	ch := make(chan *Block, 1)

	go func() {
		defer close(ch)
		lastHeight := fromHeight
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}

			height, err := c.GetBestBlockHeight()
			if err != nil || height <= lastHeight {
				continue
			}

			for h := lastHeight + 1; h <= height; h++ {
				block, err := c.GetBlockByHeight(h)
				if err != nil {
					continue
				}
				select {
				case ch <- block:
				case <-ctx.Done():
					return
				}
			}
			lastHeight = height
		}
	}()

	return ch, nil
}

func hexToBytes(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		s = "0" + s
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(b); i++ {
		b[i] = hexByte(s[i*2])<<4 | hexByte(s[i*2+1])
	}
	return b, nil
}

func hexByte(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func bytesToHex(b []byte) string {
	const hextable = "0123456789abcdef"
	buf := make([]byte, len(b)*2)
	for i, v := range b {
		buf[i*2] = hextable[v>>4]
		buf[i*2+1] = hextable[v&0x0f]
	}
	return string(buf)
}
