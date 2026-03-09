package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// tylpush embeds Tylium envelope(s) in Bitcoin transactions and optionally
// mines a block.
//
// Embedding methods:
//   - Small envelopes (≤80 bytes): OP_RETURN output (provable burn)
//   - Large envelopes (>80 bytes): P2WSH witness data (segwit discount)
//
// Usage:
//   tylpush [options] <envelope_hex> [<envelope_hex>...]

var (
	btcRPC  = "http://127.0.0.1:18332"
	btcUser = "tylium"
	btcPass = "tylium"
	wallet  = ""
	doMine  = true
)

const maxPushSize = 520
const opReturnMaxData = 80

func main() {
	envelopes := parseFlags(os.Args[1:])
	if len(envelopes) == 0 {
		usage()
	}

	for i, envHex := range envelopes {
		envHex = strings.TrimPrefix(envHex, "0x")
		txid := pushEnvelope(envHex)
		fmt.Printf("[%d/%d] btc txid: %s\n", i+1, len(envelopes), txid)
	}

	if doMine {
		addr := btcCall("getnewaddress").(string)
		btcCall("generatetoaddress", 1, addr)
		height := btcCall("getblockcount")
		fmt.Printf("mined block %v\n", height)
	}
}

func pushEnvelope(envHex string) string {
	envBytes, err := hex.DecodeString(envHex)
	if err != nil {
		fatal("invalid envelope hex: %v", err)
	}

	if len(envBytes) <= opReturnMaxData {
		return embedOpReturn(envBytes)
	}
	return embedWitness(envBytes)
}

// embedOpReturn embeds a small envelope in an OP_RETURN output.
func embedOpReturn(envelope []byte) string {
	script := buildOpReturnScript(envelope)
	raw := buildOpReturnTx(script)
	fundResult := btcCall("fundrawtransaction", hex.EncodeToString(raw))
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult := btcCall("signrawtransactionwithwallet", fundedHex)
	signedHex := signResult.(map[string]interface{})["hex"].(string)
	txid := btcCall("sendrawtransaction", signedHex).(string)
	return txid
}

// embedWitness embeds a large envelope in P2WSH witness data.
func embedWitness(envelope []byte) string {
	chunks := splitEnvelope(envelope)
	witnessScript := buildWitnessScript(len(chunks))

	scriptHash := sha256.Sum256(witnessScript)
	p2wshScript := make([]byte, 34)
	p2wshScript[0] = 0x00
	p2wshScript[1] = 0x20
	copy(p2wshScript[2:], scriptHash[:])

	const embedSats uint64 = 546
	fundingTxRaw := buildFundingTx(p2wshScript, embedSats)
	fundResult := btcCall("fundrawtransaction", hex.EncodeToString(fundingTxRaw))
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult := btcCall("signrawtransactionwithwallet", fundedHex)
	signedHex := signResult.(map[string]interface{})["hex"].(string)
	fundingTxid := btcCall("sendrawtransaction", signedHex).(string)

	p2wshVout := findP2WSHOutput(signedHex, p2wshScript)

	spendingTxRaw := buildSpendingTx(fundingTxid, p2wshVout, chunks, witnessScript)
	spendTxid := btcCall("sendrawtransaction", hex.EncodeToString(spendingTxRaw)).(string)
	return spendTxid
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
		end := maxPushSize
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

func buildSpendingTx(fundingTxid string, vout int, chunks [][]byte, witnessScript []byte) []byte {
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

	// Witness: [chunk0, chunk1, ..., witnessScript]
	buf.WriteByte(byte(len(chunks) + 1))
	for _, chunk := range chunks {
		writeVarInt(&buf, uint64(len(chunk)))
		buf.Write(chunk)
	}
	writeVarInt(&buf, uint64(len(witnessScript)))
	buf.Write(witnessScript)

	binary.Write(&buf, binary.LittleEndian, uint32(0)) // locktime
	return buf.Bytes()
}

func findP2WSHOutput(txHex string, wantScript []byte) int {
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
	if params == nil {
		params = []interface{}{}
	}
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})

	url := btcRPC
	if wallet != "" {
		url += "/wallet/" + wallet
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		fatal("http request: %v", err)
	}
	req.SetBasicAuth(btcUser, btcPass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("bitcoin rpc: %v", err)
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
		fatal("decode rpc response: %v", err)
	}
	if rpcResp.Error != nil {
		fatal("bitcoin rpc %s: [%d] %s", method, rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result
}

// --- Flags ---

func parseFlags(args []string) []string {
	var envelopes []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--btcrpc":
			i++
			btcRPC = args[i]
		case "--btcuser":
			i++
			btcUser = args[i]
		case "--btcpass":
			i++
			btcPass = args[i]
		case "--wallet":
			i++
			wallet = args[i]
		case "--no-mine":
			doMine = false
		case "--mine":
			doMine = true
		case "-h", "--help":
			usage()
		default:
			envelopes = append(envelopes, args[i])
		}
	}
	return envelopes
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: tylpush [options] <envelope_hex> [<envelope_hex>...]

Embeds Tylium envelope(s) in Bitcoin transactions and mines a regtest block.

Method:
  Small envelopes (≤80 bytes, e.g. deposits): OP_RETURN output (provable burn)
  Large envelopes (>80 bytes, e.g. deploys):   P2WSH witness data

Options:
  --btcrpc URL       Bitcoin Core RPC (default: http://127.0.0.1:18332)
  --btcuser USER     Bitcoin RPC username (default: tylium)
  --btcpass PASS     Bitcoin RPC password (default: tylium)
  --wallet NAME      Bitcoin Core wallet name
  --no-mine          Don't auto-mine after submitting
  --mine             Auto-mine a block (default)

Example:
  tylpush --btcuser tylium --btcpass tylium \
    $(tylcli deposit --to alice --amount 100000 | grep envelope | awk '{print $2}')`)
	os.Exit(1)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
