package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/djkazic/tylium/pkg/bitcoin"
)

// Bitcoin envelope embedding for Tylium.
//
// Small envelopes (≤80 bytes, e.g. deposits) use OP_RETURN for provable burns.
// Large envelopes (>80 bytes, e.g. deploys, calls) use P2WSH witness embedding.

const maxTapscriptStackItem = 80 // Bitcoin Core standardness limit
const opReturnMaxData = 80
const defaultMaxFeeRate = 0.10 // BTC/kvB — sendrawtransaction default
const maxBurnBTC        = 1.0  // sendrawtransaction maxburnamount parameter

// push embeds an envelope in a Bitcoin tx (without mining a block).
// burnSats is the BTC value (in sats) to attach to the OP_RETURN output.
// For deposits, this burns real BTC. For other envelopes, pass 0.
// Returns the L1 (Bitcoin) txid as a hex string.
func push(envelope []byte, burnSats uint64) string {
	ensureWallet()

	if len(envelope) <= opReturnMaxData {
		return embedOpReturn(envelope, burnSats)
	} else {
		return embedWitness(envelope)
	}
}

// embedOpReturn embeds a small envelope in an OP_RETURN output.
// burnSats sets the output value — for deposits, this burns real BTC.
// Returns the L1 txid.
func embedOpReturn(envelope []byte, burnSats uint64) string {
	script := buildOpReturnScript(envelope)
	raw := buildOpReturnTxWithValue(script, burnSats)
	fundResult := btcCall("fundrawtransaction", hex.EncodeToString(raw))
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult := btcCall("signrawtransactionwithwallet", fundedHex)
	signedHex := signResult.(map[string]interface{})["hex"].(string)
	l1txid := btcCall("sendrawtransaction", signedHex, defaultMaxFeeRate, maxBurnBTC).(string)
	return l1txid
}

// embedWitness embeds a large envelope in a Taproot (P2TR) script-path spend.
// Returns the L1 txid of the spending transaction (which carries the data).
func embedWitness(envelope []byte) string {
	chunks := splitChunks(envelope)
	tapScript := makeWitnessScript(len(chunks))

	p2trScript, controlBlock, err := bitcoin.ComputeTaprootOutput(tapScript)
	if err != nil {
		fatal("compute taproot output: %v", err)
	}

	const embedSats = 546 // dust threshold
	fundingRaw := buildFundingTx(p2trScript, embedSats)
	fundResult := btcCall("fundrawtransaction", hex.EncodeToString(fundingRaw))
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult := btcCall("signrawtransactionwithwallet", fundedHex)
	signedHex := signResult.(map[string]interface{})["hex"].(string)
	fundingTxid := btcCall("sendrawtransaction", signedHex, defaultMaxFeeRate, maxBurnBTC).(string)

	vout := findP2WSHVout(signedHex, p2trScript)

	spendingRaw := buildTaprootSpendTx(fundingTxid, vout, chunks, tapScript, controlBlock)
	l1txid := btcCall("sendrawtransaction", hex.EncodeToString(spendingRaw), defaultMaxFeeRate, maxBurnBTC).(string)
	return l1txid
}

func ensureWallet() {
	if btcWalletURL != "" {
		return
	}
	name := "tylium"
	if btcWallet != "" {
		name = btcWallet
	}
	// Try to create; if exists, load.
	btcCallRaw(btcRPC, "createwallet", name)
	btcCallRaw(btcRPC, "loadwallet", name)
	btcWalletURL = btcRPC + "/wallet/" + name
}

func buildOpReturnScript(data []byte) []byte {
	var script []byte
	script = append(script, 0x6a) // OP_RETURN
	if len(data) <= 75 {
		script = append(script, byte(len(data)))
	} else {
		script = append(script, 0x4c, byte(len(data))) // OP_PUSHDATA1
	}
	script = append(script, data...)
	return script
}

func buildOpReturnTxWithValue(scriptPubKey []byte, sats uint64) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(2))    // version
	buf.WriteByte(0x00)                                    // 0 inputs (fundrawtransaction adds them)
	buf.WriteByte(0x01)                                    // 1 output
	binary.Write(&buf, binary.LittleEndian, sats)          // burned sats (unspendable OP_RETURN)
	writeVarInt(&buf, uint64(len(scriptPubKey)))
	buf.Write(scriptPubKey)
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // locktime
	return buf.Bytes()
}

func splitChunks(data []byte) [][]byte {
	var chunks [][]byte
	for len(data) > 0 {
		end := maxTapscriptStackItem
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[:end])
		data = data[end:]
	}
	return chunks
}

func makeWitnessScript(numChunks int) []byte {
	script := make([]byte, numChunks+1)
	for i := 0; i < numChunks; i++ {
		script[i] = 0x75 // OP_DROP
	}
	script[numChunks] = 0x51 // OP_TRUE
	return script
}

func buildFundingTx(scriptPubKey []byte, satoshis uint64) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(2))
	buf.WriteByte(0x00) // 0 inputs
	buf.WriteByte(0x01) // 1 output
	binary.Write(&buf, binary.LittleEndian, satoshis)
	writeVarInt(&buf, uint64(len(scriptPubKey)))
	buf.Write(scriptPubKey)
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	return buf.Bytes()
}

// buildTaprootSpendTx builds a Taproot script-path spending transaction.
// Witness: [chunk0, chunk1, ..., tapScript, controlBlock]
func buildTaprootSpendTx(fundingTxid string, vout int, chunks [][]byte, tapScript, controlBlock []byte) []byte {
	prevHash := reverseTxid(fundingTxid)

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(2))
	buf.Write([]byte{0x00, 0x01}) // segwit marker+flag
	buf.WriteByte(0x01)           // 1 input

	buf.Write(prevHash)
	binary.Write(&buf, binary.LittleEndian, uint32(vout))
	buf.WriteByte(0x00) // empty scriptSig
	binary.Write(&buf, binary.LittleEndian, uint32(0xffffffff))

	// 1 OP_RETURN output (0 sats). Script is padded to 22 bytes so the
	// non-witness tx size meets Bitcoin's 82-byte minimum.
	buf.WriteByte(0x01)                                // 1 output
	binary.Write(&buf, binary.LittleEndian, uint64(0)) // 0 sats
	buf.WriteByte(22)                                  // script length
	buf.WriteByte(0x6a)                                // OP_RETURN
	buf.WriteByte(0x14)                                // push 20 bytes
	buf.Write(make([]byte, 20))                        // padding

	// Witness: [chunk0, chunk1, ..., tapScript, controlBlock]
	witnessItems := len(chunks) + 2
	buf.WriteByte(byte(witnessItems))
	for _, chunk := range chunks {
		writeVarInt(&buf, uint64(len(chunk)))
		buf.Write(chunk)
	}
	writeVarInt(&buf, uint64(len(tapScript)))
	buf.Write(tapScript)
	writeVarInt(&buf, uint64(len(controlBlock)))
	buf.Write(controlBlock)

	binary.Write(&buf, binary.LittleEndian, uint32(0))
	return buf.Bytes()
}

func findP2WSHVout(txHex string, wantScript []byte) int {
	decoded := btcCall("decoderawtransaction", txHex)
	vouts := decoded.(map[string]interface{})["vout"].([]interface{})
	wantHex := hex.EncodeToString(wantScript)
	for _, v := range vouts {
		vm := v.(map[string]interface{})
		spk := vm["scriptPubKey"].(map[string]interface{})
		if spk["hex"].(string) == wantHex {
			return int(vm["n"].(float64))
		}
	}
	fatal("P2WSH output not found in funding transaction")
	return -1
}

func reverseTxid(txid string) []byte {
	b, _ := hex.DecodeString(txid)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return b
}

func writeVarInt(buf *bytes.Buffer, v uint64) {
	if v < 0xfd {
		buf.WriteByte(byte(v))
	} else if v <= 0xffff {
		buf.WriteByte(0xfd)
		binary.Write(buf, binary.LittleEndian, uint16(v))
	} else if v <= 0xffffffff {
		buf.WriteByte(0xfe)
		binary.Write(buf, binary.LittleEndian, uint32(v))
	} else {
		buf.WriteByte(0xff)
		binary.Write(buf, binary.LittleEndian, v)
	}
}

// --- Bitcoin RPC ---

func btcCall(method string, params ...interface{}) interface{} {
	result, err := btcCallRaw(btcWalletURL, method, params...)
	if err != nil {
		fatal("bitcoin rpc %s: %v\n  Is Bitcoin Core running at %s?", method, err, btcRPC)
	}
	return result
}

func btcCallRaw(endpoint, method string, params ...interface{}) (interface{}, error) {
	if params == nil {
		params = []interface{}{}
	}
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(btcUser, btcPass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%v", err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("[%d] %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}
