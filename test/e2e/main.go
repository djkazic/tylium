package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/djkazic/tylium/contracts"
	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/types"
)

// End-to-end test: Bitcoin regtest → Tylium bridge → tokens → AMM → swap.
//
// Expects bitcoin and tyld to be running (docker-compose).
// Exits 0 on success, 1 on failure.

var (
	btcRPC    = envOr("BTC_RPC", "http://bitcoin:18443")
	tylRPC    = envOr("TYL_RPC", "http://tyld:19332")
	btcUser   = envOr("BTC_USER", "tylium")
	btcPass   = envOr("BTC_PASS", "tylium")
	btcWallet = "" // set after wallet creation
)

// maxTapscriptStackItem is Bitcoin Core's standardness limit for tapscript witness items.
const maxTapscriptStackItem = 80

func main() {
	fmt.Println("=== Tylium E2E Test ===")
	fmt.Println()

	// --- Setup ---
	step("Waiting for Bitcoin RPC", func() {
		waitForBitcoin()
	})

	step("Creating Bitcoin wallet", func() {
		// Try to create; if it already exists, load it.
		_, err := btcCallRaw(btcRPC, "createwallet", "e2e")
		if err != nil {
			btcCallRaw(btcRPC, "loadwallet", "e2e")
		}
		btcWallet = btcRPC + "/wallet/e2e"
	})

	step("Mining 101 blocks for spendable coins", func() {
		addr := btcWalletCall("getnewaddress").(string)
		btcWalletCall("generatetoaddress", 101, addr)
		height := btcWalletCall("getblockcount")
		fmt.Printf("  bitcoin height: %v\n", height)
	})

	step("Waiting for tyld to sync", func() {
		tip := uint64(btcWalletCall("getblockcount").(float64))
		waitForTylHeight(tip)
		fmt.Printf("  tyld synced to height %d\n", tip)
	})

	// --- Generate L2 key ---
	alice, err := crypto.GenerateKey()
	if err != nil {
		fatalf("keygen: %v", err)
	}
	aliceAddr := alice.Public().Address()
	aliceID := binary.BigEndian.Uint64(aliceAddr[12:20])
	fmt.Printf("\nalice address: %s (caller_id: %d)\n\n", aliceAddr.Hex(), aliceID)

	// ============================================================
	// Step 1: Bridge Bitcoin → Tylium
	// ============================================================
	step("Bridging 10,000,000 sats to alice (deposit)", func() {
		env := buildDeposit(aliceAddr, 10_000_000)
		embedWithBurn(env, 10_000_000)
	})

	step("Verifying alice L2 balance", func() {
		bal := tylGetBalance(aliceAddr)
		assertEqual("alice balance", uint64(10_000_000), bal)
	})

	// ============================================================
	// Step 2: Deploy $POOP token
	// ============================================================
	tokenCode := contracts.BuildTokenContract(aliceID, 10_000_000)
	tokenAddr := executor.DeriveContractAddress(aliceAddr, 0)
	fmt.Printf("$POOP token will deploy to: %s\n\n", tokenAddr.Hex())

	step("Deploying $POOP token contract", func() {
		env := buildTx(alice, 0, types.ZeroAddress, 0, 200_000, tokenCode)
		embed(env)
	})

	step("Verifying $POOP contract exists", func() {
		code := tylGetCode(tokenAddr)
		if code == "" {
			fatalf("no code at token address %s", tokenAddr.Hex())
		}
		fmt.Printf("  code length: %d bytes\n", len(code)/2)
	})

	// ============================================================
	// Step 3: Mint 1,000,000 $POOP to alice
	// ============================================================
	step("Minting 1,000,000 $POOP to alice", func() {
		data := packCallData(contracts.FnMint, 1_000_000)
		env := buildTx(alice, 1, tokenAddr, 0, 500_000, data)
		embed(env)
	})

	step("Verifying alice $POOP balance", func() {
		balSlot := uint64(contracts.SlotBalanceBase) + aliceID
		bal := tylGetStorageUint64(tokenAddr, balSlot)
		assertEqual("alice POOP balance", uint64(1_000_000), bal)
	})

	// ============================================================
	// Step 4: Deploy AMM pool ($POOP / tyBTC, 0.3% fee)
	// ============================================================
	ammCode := contracts.BuildAMMContract(997, tokenAddr.CallerID())
	ammAddr := executor.DeriveContractAddress(aliceAddr, 2)
	fmt.Printf("AMM pool will deploy to: %s\n\n", ammAddr.Hex())

	step("Deploying AMM pool (0.3%% fee)", func() {
		env := buildTx(alice, 2, types.ZeroAddress, 0, 200_000, ammCode)
		embed(env)
	})

	step("Verifying AMM contract exists", func() {
		code := tylGetCode(ammAddr)
		if code == "" {
			fatalf("no code at AMM address %s", ammAddr.Hex())
		}
		fmt.Printf("  code length: %d bytes\n", len(code)/2)
	})

	// ============================================================
	// Step 5: Add liquidity (100,000 POOP + 100,000 tyBTC)
	// ============================================================
	step("Adding liquidity: 100,000 POOP + 100,000 tyBTC", func() {
		data := packCallData(contracts.AMMFnAddLiquidity, 100_000, 100_000)
		env := buildTx(alice, 3, ammAddr, 0, 1_000_000, data)
		embed(env)
	})

	step("Verifying AMM reserves", func() {
		resA := tylGetStorageUint64(ammAddr, contracts.AMMSlotReserveA)
		resB := tylGetStorageUint64(ammAddr, contracts.AMMSlotReserveB)
		assertEqual("reserveA (POOP)", uint64(100_000), resA)
		assertEqual("reserveB (tyBTC)", uint64(100_000), resB)
	})

	// ============================================================
	// Step 6: Swap 10,000 POOP → tyBTC
	// ============================================================
	step("Swapping 10,000 POOP for tyBTC", func() {
		data := packCallData(contracts.AMMFnSwapAForB, 10_000)
		env := buildTx(alice, 4, ammAddr, 0, 1_000_000, data)
		embed(env)
	})

	step("Verifying swap results", func() {
		resA := tylGetStorageUint64(ammAddr, contracts.AMMSlotReserveA)
		resB := tylGetStorageUint64(ammAddr, contracts.AMMSlotReserveB)

		// Expected: amountOut = 100000 * (10000*997) / (100000*1000 + 10000*997)
		//         = 100000 * 9970000 / 109970000 = 9066
		// reserveA = 100000 + 10000 = 110000
		// reserveB = 100000 - 9066 = 90934
		expectedA := uint64(110_000)
		expectedB := uint64(100_000 - 9066) // 90934

		assertEqual("reserveA after swap", expectedA, resA)
		assertEqual("reserveB after swap", expectedB, resB)

		// Verify constant product didn't decrease (k should increase from fees).
		kBefore := uint64(100_000) * uint64(100_000) // 10,000,000,000
		kAfter := resA * resB
		fmt.Printf("  k before: %d\n", kBefore)
		fmt.Printf("  k after:  %d\n", kAfter)
		if kAfter < kBefore {
			fatalf("constant product decreased: %d < %d", kAfter, kBefore)
		}

		// Check swap output in return slot 200.
		amountOut := tylGetStorageUint64(ammAddr, 200)
		fmt.Printf("  swap output: %d tyBTC for 10,000 POOP\n", amountOut)
		if amountOut == 0 {
			fatalf("swap output is 0")
		}
	})

	// ============================================================
	// Done
	// ============================================================
	fmt.Println()
	fmt.Println("=== ALL TESTS PASSED ===")
}

// --- Tylium transaction builders ---

func buildDeposit(recipient types.Address, amount uint64) []byte {
	payload := encoding.EncodeDeposit(recipient, amount)
	return encoding.EncodeEnvelope(encoding.EnvelopeTypeDeposit, payload)
}

func buildTx(key *crypto.PrivateKey, nonce uint64, to types.Address, value, gasLimit uint64, data []byte) []byte {
	tx := &types.Transaction{
		Version:  1,
		Nonce:    nonce,
		From:     key.Public().Address(),
		To:       to,
		Value:    value,
		GasPrice: 1,
		GasLimit: gasLimit,
		Data:     data,
	}
	sigHash := tx.SigningHash()
	sig, err := crypto.Sign(sigHash, key)
	if err != nil {
		fatalf("sign tx: %v", err)
	}
	tx.Signature = sig
	encoded := encoding.EncodeTx(tx)
	return encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoded)
}

func packCallData(args ...uint64) []byte {
	data := make([]byte, len(args)*8)
	for i, arg := range args {
		binary.BigEndian.PutUint64(data[i*8:], arg)
	}
	return data
}

// --- Bitcoin envelope embedding ---
//
// Small envelopes (≤80 bytes, e.g. deposits): OP_RETURN output (provable burn)
// Large envelopes (>80 bytes, e.g. deploys):   P2WSH witness data

const opReturnMaxData = 80

func embed(envelope []byte) {
	embedWithBurn(envelope, 0)
}

func embedWithBurn(envelope []byte, burnSats uint64) {
	if len(envelope) <= opReturnMaxData {
		embedOpReturn(envelope, burnSats)
	} else {
		embedWitness(envelope)
	}
	mineAndWait()
}

// embedOpReturn embeds a small envelope in an OP_RETURN output.
// burnSats sets the output value — for deposits, this burns real BTC.
func embedOpReturn(envelope []byte, burnSats uint64) {
	script := buildOpReturnScript(envelope)
	raw := buildOpReturnTxWithValue(script, burnSats)
	fundResult := btcWalletCall("fundrawtransaction", hex.EncodeToString(raw))
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult := btcWalletCall("signrawtransactionwithwallet", fundedHex)
	signedHex := signResult.(map[string]interface{})["hex"].(string)
	btcWalletCall("sendrawtransaction", signedHex, 0.10, 1.0)
}

// embedWitness embeds a large envelope via Taproot (P2TR) script-path spend.
func embedWitness(envelope []byte) {
	chunks := splitEnvelope(envelope)
	tapScript := buildWitnessScript(len(chunks))

	p2trScript, controlBlock, err := bitcoin.ComputeTaprootOutput(tapScript)
	if err != nil {
		fatalf("compute taproot output: %v", err)
	}

	const embedSats uint64 = 546
	fundingRaw := buildFundingTx(p2trScript, embedSats)
	fundResult := btcWalletCall("fundrawtransaction", hex.EncodeToString(fundingRaw))
	fundedHex := fundResult.(map[string]interface{})["hex"].(string)

	signResult := btcWalletCall("signrawtransactionwithwallet", fundedHex)
	signedHex := signResult.(map[string]interface{})["hex"].(string)
	fundingTxid := btcWalletCall("sendrawtransaction", signedHex, 0.10, 1.0).(string)

	vout := findP2WSHVout(signedHex, p2trScript)

	spendingRaw := buildTaprootSpendingTx(fundingTxid, vout, chunks, tapScript, controlBlock)
	btcWalletCall("sendrawtransaction", hex.EncodeToString(spendingRaw), 0.10, 1.0)
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

func buildOpReturnTxWithValue(scriptPubKey []byte, satoshis uint64) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(2))
	buf.WriteByte(0x00)                                    // 0 inputs
	buf.WriteByte(0x01)                                    // 1 output
	binary.Write(&buf, binary.LittleEndian, satoshis)      // burn value
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
	buf.WriteByte(0x00)
	buf.WriteByte(0x01)
	binary.Write(&buf, binary.LittleEndian, satoshis)
	writeVarInt(&buf, uint64(len(scriptPubKey)))
	buf.Write(scriptPubKey)
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	return buf.Bytes()
}

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
	decoded := btcWalletCall("decoderawtransaction", txHex)
	vouts := decoded.(map[string]interface{})["vout"].([]interface{})
	wantHex := hex.EncodeToString(wantScript)
	for _, v := range vouts {
		vm := v.(map[string]interface{})
		spk := vm["scriptPubKey"].(map[string]interface{})
		if spk["hex"].(string) == wantHex {
			return int(vm["n"].(float64))
		}
	}
	fatalf("P2WSH output not found in funding transaction")
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

// --- Wait helpers ---

func mineAndWait() {
	addr := btcWalletCall("getnewaddress").(string)
	btcWalletCall("generatetoaddress", 1, addr)
	tip := uint64(btcWalletCall("getblockcount").(float64))
	waitForTylHeight(tip)
}

func waitForBitcoin() {
	for i := 0; i < 60; i++ {
		resp, err := btcCallRaw(btcRPC, "getblockchaininfo")
		if err == nil && resp != nil {
			return
		}
		time.Sleep(time.Second)
	}
	fatalf("timeout waiting for bitcoin")
}

func waitForTylHeight(minHeight uint64) {
	for i := 0; i < 120; i++ {
		h := tylHeight()
		if h >= minHeight {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	fatalf("timeout waiting for tyld height >= %d", minHeight)
}

func tylHeight() uint64 {
	result := tylCall("tyl_blockHeight", []interface{}{})
	if result == nil {
		return 0
	}
	var hr struct {
		Height uint64 `json:"height"`
	}
	json.Unmarshal(result, &hr)
	return hr.Height
}

// --- Tylium RPC ---

func tylGetBalance(addr types.Address) uint64 {
	result := tylCall("tyl_getBalance", []string{addr.Hex()})
	var br struct {
		Balance uint64 `json:"balance"`
	}
	json.Unmarshal(result, &br)
	return br.Balance
}

func tylGetCode(addr types.Address) string {
	result := tylCall("tyl_getCode", []string{addr.Hex()})
	var cr struct {
		Code string `json:"code"`
	}
	json.Unmarshal(result, &cr)
	return cr.Code
}

func tylGetStorageUint64(addr types.Address, slot uint64) uint64 {
	var key [32]byte
	binary.BigEndian.PutUint64(key[24:], slot)
	keyHex := hex.EncodeToString(key[:])

	result := tylCall("tyl_getStorage", []string{addr.Hex(), keyHex})
	var sr struct {
		Value string `json:"value"`
	}
	json.Unmarshal(result, &sr)

	valBytes, _ := hex.DecodeString(strings.TrimPrefix(sr.Value, "0x"))
	if len(valBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(valBytes):], valBytes)
		valBytes = padded
	}
	return binary.BigEndian.Uint64(valBytes[24:32])
}

func tylCall(method string, params interface{}) json.RawMessage {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	resp, err := http.Post(tylRPC, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&rpcResp)
	if rpcResp.Error != nil {
		fatalf("tyld rpc %s: %s", method, rpcResp.Error.Message)
	}
	return rpcResp.Result
}

// --- Bitcoin RPC ---

func btcCallBase(method string, params ...interface{}) interface{} {
	result, err := btcCallRaw(btcRPC, method, params...)
	if err != nil {
		// Ignore errors for createwallet (may already exist).
		if method == "createwallet" {
			return nil
		}
		fatalf("bitcoin rpc %s: %v", method, err)
	}
	return result
}

func btcWalletCall(method string, params ...interface{}) interface{} {
	result, err := btcCallRaw(btcWallet, method, params...)
	if err != nil {
		fatalf("bitcoin rpc %s: %v", method, err)
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
		return nil, err
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

// --- Utility ---

func step(name string, fn func()) {
	fmt.Printf("• %s ... ", name)
	fn()
	fmt.Println("OK")
}

func assertEqual(label string, expected, actual uint64) {
	if expected != actual {
		fatalf("%s: expected %d, got %d", label, expected, actual)
	}
	fmt.Printf("  %s = %d ✓\n", label, actual)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fatalf(format string, args ...interface{}) {
	fmt.Printf("FAIL\n")
	fmt.Fprintf(os.Stderr, "\nFAILED: "+format+"\n", args...)
	os.Exit(1)
}
