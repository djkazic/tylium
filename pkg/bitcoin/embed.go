package bitcoin

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// maxTapscriptStackItem is the Bitcoin Core standardness limit for individual
// tapscript witness stack items (MAX_STANDARD_TAPSCRIPT_STACK_ITEM_SIZE).
// Consensus allows 520 bytes, but policy restricts to 80.
const maxTapscriptStackItem = 80
const opReturnMaxData = 80
const defaultMaxFeeRate = 0.10 // BTC/kvB — sendrawtransaction default
const maxBurnAmount     = 1.0  // BTC — sendrawtransaction maxburnamount parameter
const targetFeeRate     = 1    // sat/vB for the spending tx

// EmbedEnvelope embeds a Tylium envelope in a Bitcoin transaction and broadcasts it.
// Small envelopes (<=80 bytes) use OP_RETURN; larger ones use P2WSH witness data.
// Returns the Bitcoin txid hex string. Requires a funded wallet on the node.
func EmbedEnvelope(c *RPCClient, envelope []byte) (string, error) {
	if len(envelope) <= opReturnMaxData {
		return embedOpReturn(c, envelope)
	}
	return embedWitness(c, envelope)
}

func embedOpReturn(c *RPCClient, envelope []byte) (string, error) {
	script := buildOpReturnScript(envelope)
	raw := buildOpReturnTx(script)

	fundResult, err := c.callJSON("fundrawtransaction", hex.EncodeToString(raw))
	if err != nil {
		return "", fmt.Errorf("fund tx: %w", err)
	}
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult, err := c.callJSON("signrawtransactionwithwallet", fundedHex)
	if err != nil {
		return "", fmt.Errorf("sign tx: %w", err)
	}
	signedHex := signResult.(map[string]interface{})["hex"].(string)

	txidResult, err := c.callJSON("sendrawtransaction", signedHex, defaultMaxFeeRate, maxBurnAmount)
	if err != nil {
		return "", fmt.Errorf("send tx: %w", err)
	}
	return txidResult.(string), nil
}

func embedWitness(c *RPCClient, envelope []byte) (string, error) {
	chunks := splitEnvelope(envelope)
	tapScript := buildWitnessScript(len(chunks))

	p2trScript, controlBlock, err := ComputeTaprootOutput(tapScript)
	if err != nil {
		return "", fmt.Errorf("compute taproot output: %w", err)
	}

	embedSats := spendingTxFee(chunks, controlBlock, tapScript)
	fundingTxRaw := buildFundingTx(p2trScript, embedSats)
	fundResult, err := c.callJSON("fundrawtransaction", hex.EncodeToString(fundingTxRaw))
	if err != nil {
		return "", fmt.Errorf("fund funding tx: %w", err)
	}
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult, err := c.callJSON("signrawtransactionwithwallet", fundedHex)
	if err != nil {
		return "", fmt.Errorf("sign funding tx: %w", err)
	}
	signedHex := signResult.(map[string]interface{})["hex"].(string)

	txidResult, err := c.callJSON("sendrawtransaction", signedHex, defaultMaxFeeRate, maxBurnAmount)
	if err != nil {
		return "", fmt.Errorf("send funding tx: %w", err)
	}
	fundingTxid := txidResult.(string)

	vout, err := findP2WSHOutput(c, signedHex, p2trScript)
	if err != nil {
		return "", err
	}

	spendingTxRaw := buildTaprootSpendingTx(fundingTxid, vout, chunks, tapScript, controlBlock)
	spendResult, err := c.callJSON("sendrawtransaction", hex.EncodeToString(spendingTxRaw), defaultMaxFeeRate, maxBurnAmount)
	if err != nil {
		return "", fmt.Errorf("send spending tx: %w", err)
	}
	return spendResult.(string), nil
}

// callJSON is like call but returns the result as interface{} for map access.
// Uses the wallet endpoint for wallet-specific operations.
func (c *RPCClient) callJSON(method string, params ...interface{}) (interface{}, error) {
	result, err := c.callEndpoint(c.walletEndpoint(), method, params...)
	if err != nil {
		return nil, err
	}
	var v interface{}
	if err := json.Unmarshal(result, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// MineBlock mines a single regtest block.
func (c *RPCClient) MineBlock() error {
	addrResult, err := c.callJSON("getnewaddress")
	if err != nil {
		return fmt.Errorf("getnewaddress: %w", err)
	}
	_, err = c.callJSON("generatetoaddress", 1, addrResult.(string))
	return err
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

func buildOpReturnTx(scriptPubKey []byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(2))
	buf.WriteByte(0x00)                                 // 0 inputs
	buf.WriteByte(0x01)                                 // 1 output
	binary.Write(&buf, binary.LittleEndian, uint64(0))  // 0 sats
	writeVarInt(&buf, uint64(len(scriptPubKey)))
	buf.Write(scriptPubKey)
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	return buf.Bytes()
}

func splitEnvelope(envelope []byte) [][]byte {
	var chunks [][]byte
	for len(envelope) > 0 {
		end := maxTapscriptStackItem
		if end > len(envelope) {
			end = len(envelope)
		}
		chunks = append(chunks, envelope[:end])
		envelope = envelope[end:]
	}
	return chunks
}

func buildWitnessScript(numChunks int) []byte {
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

// buildTaprootSpendingTx builds a transaction that spends a P2TR output via
// script-path, embedding data chunks in the witness stack.
// Witness: [chunk0, chunk1, ..., tapScript, controlBlock]
func buildTaprootSpendingTx(fundingTxid string, vout int, chunks [][]byte, tapScript, controlBlock []byte) []byte {
	prevHash := reverseTxid(fundingTxid)

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(2))
	buf.Write([]byte{0x00, 0x01}) // segwit marker+flag
	buf.WriteByte(0x01)           // 1 input

	buf.Write(prevHash)
	binary.Write(&buf, binary.LittleEndian, uint32(vout))
	buf.WriteByte(0x00) // empty scriptSig
	binary.Write(&buf, binary.LittleEndian, uint32(0xffffffff))

	// 1 OP_RETURN output (0 sats). Script must be >= 5 bytes so the
	// non-witness tx size meets Bitcoin Core's 65-byte minimum.
	buf.WriteByte(0x01)                                // 1 output
	binary.Write(&buf, binary.LittleEndian, uint64(0)) // 0 sats
	buf.WriteByte(0x05)                                // script length = 5
	buf.WriteByte(0x6a)                                // OP_RETURN
	buf.WriteByte(0x03)                                // PUSH 3 bytes
	buf.Write([]byte{0x00, 0x00, 0x00})                // padding

	// Witness: [chunk0, chunk1, ..., tapScript, controlBlock]
	witnessItems := len(chunks) + 2 // chunks + script + control block
	buf.WriteByte(byte(witnessItems))
	for _, chunk := range chunks {
		writeVarInt(&buf, uint64(len(chunk)))
		buf.Write(chunk)
	}
	writeVarInt(&buf, uint64(len(tapScript)))
	buf.Write(tapScript)
	writeVarInt(&buf, uint64(len(controlBlock)))
	buf.Write(controlBlock)

	binary.Write(&buf, binary.LittleEndian, uint32(0)) // locktime
	return buf.Bytes()
}

// spendingTxFee computes the fee for the spending tx at targetFeeRate.
// The entire funding output becomes fee (output is OP_RETURN at 0 sats),
// so we set funding sats = fee, with a floor of 546 (dust limit).
func spendingTxFee(chunks [][]byte, controlBlock, tapScript []byte) uint64 {
	// Base (non-witness) size: version(4) + txin_count(1) + prevout(36) +
	// scriptSig_len(1) + sequence(4) + txout_count(1) + value(8) +
	// script_len(1) + script(5) + locktime(4) = 65
	baseSize := 65

	// Witness size: marker+flag(2) + item_count(1) + items
	witnessSize := 2 + 1 // marker, flag, item count
	for _, chunk := range chunks {
		witnessSize += varIntSize(uint64(len(chunk))) + len(chunk)
	}
	witnessSize += varIntSize(uint64(len(tapScript))) + len(tapScript)
	witnessSize += varIntSize(uint64(len(controlBlock))) + len(controlBlock)

	// weight = base_size * 4 + witness_size (marker/flag/witness are witness data)
	// Technically: weight = (total - witness) * 4 + witness = base*3 + total
	weight := baseSize*3 + (baseSize + witnessSize)
	vsize := (weight + 3) / 4

	fee := uint64(vsize) * targetFeeRate
	if fee < 546 {
		fee = 546 // dust limit floor
	}
	return fee
}

func varIntSize(v uint64) int {
	if v < 0xfd {
		return 1
	} else if v <= 0xffff {
		return 3
	} else if v <= 0xffffffff {
		return 5
	}
	return 9
}

func findP2WSHOutput(c *RPCClient, txHex string, wantScript []byte) (int, error) {
	decoded, err := c.callJSON("decoderawtransaction", txHex)
	if err != nil {
		return -1, fmt.Errorf("decode tx: %w", err)
	}
	vouts := decoded.(map[string]interface{})["vout"].([]interface{})
	wantHex := hex.EncodeToString(wantScript)
	for _, v := range vouts {
		vm := v.(map[string]interface{})
		spk := vm["scriptPubKey"].(map[string]interface{})
		if spk["hex"].(string) == wantHex {
			return int(vm["n"].(float64)), nil
		}
	}
	return -1, fmt.Errorf("P2WSH output not found in funding transaction")
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
