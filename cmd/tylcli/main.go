package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djkazic/tylium/contracts"
	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/types"
)

var (
	rpcEndpoint  = envOr("TYL_RPC", "http://127.0.0.1:19332")
	swapdEndpoint = envOr("TYL_SWAPD", "http://127.0.0.1:19333")
	swapdAPIKey   = envOr("TYL_SWAPD_KEY", "")
	btcRPC       = envOr("BTC_RPC", "http://127.0.0.1:18332")
	btcUser      = envOr("BTC_USER", "tylium")
	btcPass      = envOr("BTC_PASS", "tylium")
	btcWallet    = envOr("BTC_WALLET", "")
	btcWalletURL string // set lazily by ensureWallet

	keyDir      string
	contractDir string
	pendingDir  string
)

func init() {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	base := envOr("TYL_DATADIR", filepath.Join(home, ".tylium"))
	keyDir = filepath.Join(base, "keys")
	contractDir = filepath.Join(base, "contracts")
	pendingDir = filepath.Join(base, "pending")
}

// Known function names → selectors.
var fnSelectors = map[string]uint64{
	// Token
	"mint":      contracts.FnMint,
	"transfer":  contracts.FnTransfer,
	"balanceOf": contracts.FnBalanceOf,
	// AMM
	"addLiquidity":    contracts.AMMFnAddLiquidity,
	"removeLiquidity": contracts.AMMFnRemoveLiquidity,
	"swapAForB":       contracts.AMMFnSwapAForB,
	"swapBForA":       contracts.AMMFnSwapBForA,
}

func main() {
	args := os.Args[1:]
	args = extractGlobalFlags(args)

	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	switch args[0] {
	// High-level commands
	case "deposit":
		cmdDeposit(args[1:])
	case "deploy":
		cmdDeploy(args[1:])
	case "call":
		cmdCall(args[1:])
	// Key management
	case "keygen":
		cmdKeygen(args[1:])
	case "keys":
		cmdKeys()
	case "import-key":
		cmdImportKey(args[1:])
	case "contracts":
		cmdContracts()
	// Queries
	case "height":
		cmdHeight()
	case "finalized":
		cmdFinalized()
	case "stateroot":
		cmdStateRoot()
	case "account":
		cmdAccount(args[1:])
	case "balance":
		cmdBalance(args[1:])
	case "storage":
		cmdStorage(args[1:])
	case "code":
		cmdCode(args[1:])
	case "block":
		cmdBlock(args[1:])
	case "receipts":
		cmdReceipts(args[1:])
	case "status":
		cmdStatus(args[1:])
	case "history":
		cmdHistory(args[1:])
	// Atomic swaps (high-level)
	case "swap":
		cmdSwap(args[1:])
	// HTLC operations (low-level)
	case "htlc":
		cmdHTLC(args[1:])
	// Raw tx builder (power user)
	case "tx":
		cmdTx(args[1:])
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func extractGlobalFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rpc":
			i++
			rpcEndpoint = args[i]
		case "--datadir":
			i++
			keyDir = filepath.Join(args[i], "keys")
			contractDir = filepath.Join(args[i], "contracts")
		case "--keydir":
			i++
			keyDir = args[i]
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
			btcWallet = args[i]
		case "--swapd":
			i++
			swapdEndpoint = args[i]
		case "--swapd-key":
			i++
			swapdAPIKey = args[i]
		default:
			out = append(out, args[i])
		}
	}
	return out
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: tylcli [options] <command> [args...]

Commands:
  deposit <to> <amount>                  Bridge sats from Bitcoin to Tylium
  deploy token [--name N]                Deploy a token contract
  deploy amm [fee] [--name N]            Deploy an AMM pool (default fee: 997 = 0.3%)
  call <contract> <fn> [args...]         Call a contract function
  keygen [--name N]                      Generate a new keypair
  keys                                   List saved keys
  contracts                              List saved contracts
  status <txid>                          Check transaction status and finality
  history [--limit N]                    Show recent transactions (default: 20)
  balance <name|addr>                    Get L2 balance (shows finality status)
  account <name|addr>                    Get account info (nonce, balance, etc.)
  storage <addr> <slot>                  Read contract storage slot
  receipts [height]                      Show transaction receipts (default: latest)
  swap buy --amount <amt>                Buy tyBTC with Lightning (fully automatic)
  swap buy --resume <htlc_id>            Resume an interrupted buy swap
  swap sell --htlc <id> --invoice <inv>  Sell tyBTC for Lightning
  swap list                              List active swaps
  swap status <id>                       Check swap status
  htlc lock <to> <timelock> <hash> --value <amt>  Lock tyBTC in an HTLC
  htlc claim <id> <preimage>             Claim an HTLC by revealing the preimage
  htlc refund <id>                       Refund an expired HTLC
  htlc query <id>                        Query HTLC status
  finalized                              Show finalized vs optimistic height
  height                                 Current block height
  tx [raw flags]                         Build a raw transaction (power user)

Transaction flags (for deposit/deploy/call):
  --from <name>           Signing key (auto-detected if only one key)
  --no-push               Print envelope hex instead of broadcasting

Bitcoin connection (or use env: BTC_RPC, BTC_USER, BTC_PASS, BTC_WALLET):
  --btcrpc URL            Bitcoin RPC (default: http://127.0.0.1:18332)
  --btcuser USER          RPC user (default: tylium)
  --btcpass PASS          RPC pass (default: tylium)
  --wallet NAME           Bitcoin wallet name

Swap daemon (or use env: TYL_SWAPD, TYL_SWAPD_KEY):
  --swapd URL             Swap daemon API (default: http://127.0.0.1:19333)
  --swapd-key KEY         API key for swap daemon

Global (or use env: TYL_RPC, TYL_DATADIR):
  --rpc URL               Tylium RPC (default: http://127.0.0.1:19332)
  --datadir DIR           Data directory for keys/contracts (default: ~/.tylium)

Examples:
  tylcli keygen --name alice
  tylcli deposit alice 10000000
  tylcli deploy token --name poop
  tylcli call poop mint 1000000
  tylcli deploy amm 997 --name pool
  tylcli call pool addLiquidity 100000 100000
  tylcli call pool swapAForB 10000
  tylcli swap buy --amount 50000
  tylcli swap sell --htlc 3 --invoice lnbc...
  tylcli status <txid>
  tylcli history --limit 10
  tylcli balance alice`)
}

// ============================================================
// High-level commands
// ============================================================

func cmdDeposit(args []string) {
	pos, flags := parseFlags(args)

	// Support both positional and flag-based syntax.
	var toRef string
	var amount uint64
	if len(pos) >= 2 {
		toRef = pos[0]
		amount = mustParseUint(pos[1], "amount")
	} else {
		toRef = flags["to"]
		if v, ok := flags["amount"]; ok {
			amount = mustParseUint(v, "amount")
		}
	}
	if toRef == "" {
		fatal("usage: tylcli deposit <to> <amount>")
	}
	if amount == 0 {
		fatal("amount must be > 0")
	}

	addrHex := resolveAddress(toRef)
	addrBytes, _ := hex.DecodeString(stripHex(addrHex))
	recipient := types.BytesToAddress(addrBytes)

	payload := encoding.EncodeDeposit(recipient, amount)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeDeposit, payload)

	fmt.Printf("deposit %d sats → %s (burning %d sats on L1)\n", amount, toRef, amount)

	if flags["no-push"] == "" {
		// txid="" tells pushAndReport to compute it from the L1 txid after broadcast.
		pushAndReport(envelope, amount, "")
	} else {
		fmt.Printf("envelope: %s\n", hex.EncodeToString(envelope))
	}
}

func cmdDeploy(args []string) {
	pos, flags := parseFlags(args)
	if len(pos) < 1 {
		fatal("usage: tylcli deploy token|amm [fee] [--name NAME] [--from NAME]")
	}

	contractType := pos[0]
	var bytecode []byte

	// Need signing key early for token deploy (to derive ownerID).
	key := resolveSigningKey(flags["from"])
	addr := key.Public().Address()

	switch contractType {
	case "token":
		ownerID := addrToCallerID(addr.Hex())
		maxSupply := uint64(0) // 0 = unlimited minting
		if len(pos) >= 2 {
			maxSupply = mustParseUint(pos[1], "max_supply")
		}
		if v, ok := flags["max-supply"]; ok {
			maxSupply = mustParseUint(v, "max_supply")
		}
		bytecode = contracts.BuildTokenContract(ownerID, maxSupply)
		if maxSupply > 0 {
			fmt.Printf("max supply: %d\n", maxSupply)
		} else {
			fmt.Println("max supply: unlimited")
		}
	case "amm":
		fee := uint64(997)
		if len(pos) >= 2 {
			fee = mustParseUint(pos[1], "fee")
		}
		tokenAAddr := flags["token-a"]
		if tokenAAddr == "" {
			fatal("--token-a <contract_address> is required for AMM deployment")
		}
		tokenAID := addrToCallerID(tokenAAddr)
		bytecode = contracts.BuildAMMContract(fee, tokenAID)
		fmt.Printf("fee: %d/1000 (%.1f%%)\n", fee, float64(1000-fee)/10)
		fmt.Printf("tokenA: %s (id: %d)\n", tokenAAddr, tokenAID)
	default:
		fatal("unknown contract type: %s (expected: token, amm)", contractType)
	}

	nonce := fetchNonce(addr)

	tx := &types.Transaction{
		Version:  1,
		Nonce:    nonce,
		From:     addr,
		To:       types.ZeroAddress,
		GasPrice: 1,
		GasLimit: 200_000,
		Data:     bytecode,
	}
	signTx(tx, key)

	contractAddr := executor.DeriveContractAddress(addr, nonce)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))
	txid := tx.TxID().Hex()

	fmt.Printf("deploying %s contract...\n", contractType)
	fmt.Printf("contract: %s\n", contractAddr.Hex())

	if name, ok := flags["name"]; ok && name != "" {
		saveContract(name, contractAddr.Hex(), contractType)
		fmt.Printf("saved as: %s\n", name)
	}

	if flags["no-push"] == "" {
		pushAndReport(envelope, 0, txid)
	} else {
		fmt.Printf("txid: %s\n", txid)
		fmt.Printf("envelope: %s\n", hex.EncodeToString(envelope))
	}
}

func cmdCall(args []string) {
	pos, flags := parseFlags(args)
	if len(pos) < 2 {
		fatal("usage: tylcli call <contract> <function> [args...]\n\nFunctions: mint, transfer, balanceOf, addLiquidity, removeLiquidity,\n           swapAForB, swapBForA")
	}

	contractRef := pos[0]
	fnName := pos[1]

	// Resolve contract address.
	contractAddr := resolveContractAddr(contractRef)

	// Resolve function selector.
	selector, ok := fnSelectors[fnName]
	if !ok {
		// Try parsing as a raw integer selector.
		n, err := strconv.ParseUint(fnName, 10, 64)
		if err != nil {
			fatal("unknown function: %s\n\nKnown: %s", fnName, knownFnNames())
		}
		selector = n
	}

	// Parse arguments. Names and addresses are resolved to caller_ids.
	callArgs := []uint64{selector}
	for _, arg := range pos[2:] {
		callArgs = append(callArgs, resolveArg(arg))
	}
	data := contracts.PackCallData(callArgs...)

	key := resolveSigningKey(flags["from"])
	addr := key.Public().Address()
	nonce := fetchNonce(addr)

	var value uint64
	if v, ok := flags["value"]; ok {
		value = mustParseUint(v, "value")
	}

	tx := &types.Transaction{
		Version:  1,
		Nonce:    nonce,
		From:     addr,
		To:       contractAddr,
		Value:    value,
		GasPrice: 1,
		GasLimit: 1_000_000,
		Data:     data,
	}
	signTx(tx, key)

	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))
	txid := tx.TxID().Hex()

	fmt.Printf("%s(%s)\n", fnName, formatArgs(pos[2:]))

	if flags["no-push"] == "" {
		pushAndReport(envelope, 0, txid)
	} else {
		fmt.Printf("txid: %s\n", txid)
		fmt.Printf("envelope: %s\n", hex.EncodeToString(envelope))
	}
}

// ============================================================
// Key management
// ============================================================

type savedKey struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	PubKey  string `json:"pubkey"`
	PrivKey string `json:"privkey"`
}

func cmdKeygen(args []string) {
	_, flags := parseFlags(args)
	name := flags["name"]

	if name != "" {
		if _, err := os.Stat(keyFilePath(name)); err == nil {
			fatal("key '%s' already exists. Use a different name or 'tylcli keys' to list existing keys.", name)
		}
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		fatal("keygen error: %v", err)
	}

	sk := saveKeyToDisk(key, name)
	fmt.Printf("generated key: %s (%s)\n", sk.Name, sk.Address)
	fmt.Printf("private key:   %s\n", sk.PrivKey)
}

func cmdKeys() {
	keys := listKeys()
	if len(keys) == 0 {
		fmt.Println("no keys found (use 'tylcli keygen --name alice' to create one)")
		return
	}
	for _, k := range keys {
		fmt.Printf("%-15s %s\n", k.Name, k.Address)
	}
}

func cmdImportKey(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli import-key <privkey_hex> [--name NAME]")
	}
	pos, flags := parseFlags(args)
	privKeyHex := stripHex(pos[0])
	name := flags["name"]

	if name != "" {
		if _, err := os.Stat(keyFilePath(name)); err == nil {
			fatal("key '%s' already exists. Use a different name or 'tylcli keys' to list existing keys.", name)
		}
	}

	keyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		fatal("invalid hex: %v", err)
	}
	key, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		fatal("invalid private key: %v", err)
	}

	sk := saveKeyToDisk(key, name)
	fmt.Printf("imported key: %s (%s)\n", sk.Name, sk.Address)
}

func saveKeyToDisk(key *crypto.PrivateKey, name string) savedKey {
	pub := key.Public()
	addr := pub.Address()

	if name == "" {
		name = addr.Hex()[:12]
	}

	sk := savedKey{
		Name:    name,
		Address: addr.Hex(),
		PubKey:  hex.EncodeToString(pub.CompressedBytes()),
		PrivKey: hex.EncodeToString(key.Bytes()),
	}

	os.MkdirAll(keyDir, 0700)
	data, _ := json.MarshalIndent(sk, "", "  ")
	if err := os.WriteFile(keyFilePath(name), data, 0600); err != nil {
		fatal("write key: %v", err)
	}
	return sk
}

func keyFilePath(name string) string {
	return filepath.Join(keyDir, name+".json")
}

func listKeys() []savedKey {
	entries, _ := os.ReadDir(keyDir)
	var keys []savedKey
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(keyDir, e.Name()))
		if err != nil {
			continue
		}
		var sk savedKey
		if json.Unmarshal(data, &sk) == nil && sk.Address != "" {
			keys = append(keys, sk)
		}
	}
	return keys
}

func loadKey(nameOrAddr string) *crypto.PrivateKey {
	nameOrAddr = stripHex(nameOrAddr)

	// Try by name first.
	if data, err := os.ReadFile(keyFilePath(nameOrAddr)); err == nil {
		var sk savedKey
		if json.Unmarshal(data, &sk) == nil {
			return decodePrivKey(sk.PrivKey)
		}
	}

	// Search by address prefix.
	for _, sk := range listKeys() {
		addr := strings.TrimPrefix(strings.ToLower(sk.Address), "0x")
		query := strings.TrimPrefix(strings.ToLower(nameOrAddr), "0x")
		if strings.HasPrefix(addr, query) || strings.HasPrefix(sk.Name, nameOrAddr) {
			return decodePrivKey(sk.PrivKey)
		}
	}

	fatal("key not found: %s\n  Run 'tylcli keys' to list available keys.", nameOrAddr)
	return nil
}

func resolveSigningKey(fromRef string) *crypto.PrivateKey {
	if fromRef != "" {
		return loadKey(fromRef)
	}
	keys := listKeys()
	if len(keys) == 1 {
		return decodePrivKey(keys[0].PrivKey)
	}
	if len(keys) == 0 {
		fatal("no keys found. Run 'tylcli keygen --name alice' first.")
	}
	fatal("multiple keys found. Use --from <name>.\n  Run 'tylcli keys' to list.")
	return nil
}

func decodePrivKey(hexStr string) *crypto.PrivateKey {
	b, _ := hex.DecodeString(hexStr)
	key, err := crypto.PrivateKeyFromBytes(b)
	if err != nil {
		fatal("corrupt key file: %v", err)
	}
	return key
}

// ============================================================
// Contract registry
// ============================================================

type savedContract struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Type    string `json:"type"`
}

func saveContract(name, addr, contractType string) {
	os.MkdirAll(contractDir, 0700)
	cleanAddr := strings.ToLower(stripHex(addr))
	sc := savedContract{Name: name, Address: addr, Type: contractType}
	data, _ := json.MarshalIndent(sc, "", "  ")
	// Save by address (primary key) and name (alias pointing to latest).
	os.WriteFile(filepath.Join(contractDir, cleanAddr+".json"), data, 0644)
	os.WriteFile(filepath.Join(contractDir, "name_"+name+".json"), data, 0644)
}

func loadContract(ref string) *savedContract {
	// Try by name first.
	if sc := loadContractByName(ref); sc != nil {
		return sc
	}
	// Try by address.
	clean := strings.ToLower(stripHex(ref))
	return loadContractFile(clean + ".json")
}

func loadContractByName(name string) *savedContract {
	return loadContractFile("name_" + name + ".json")
}

func loadContractFile(filename string) *savedContract {
	data, err := os.ReadFile(filepath.Join(contractDir, filename))
	if err != nil {
		return nil
	}
	var sc savedContract
	if json.Unmarshal(data, &sc) != nil {
		return nil
	}
	return &sc
}

func cmdContracts() {
	all := listContracts()
	if len(all) == 0 {
		fmt.Println("no contracts saved (use 'tylcli deploy token --name poop' to deploy one)")
		return
	}
	seen := make(map[string]bool)
	for _, sc := range all {
		cleanAddr := strings.ToLower(stripHex(sc.Address))
		if seen[cleanAddr] {
			continue
		}
		seen[cleanAddr] = true
		name := sc.Name
		if name == "" {
			name = "-"
		}
		fmt.Printf("%s  %-8s %s\n", sc.Address, sc.Type, name)
	}
}

func resolveContractAddr(ref string) types.Address {
	// Require hex address — no aliases for call commands.
	clean := stripHex(ref)
	if len(clean) == 40 {
		b, err := hex.DecodeString(clean)
		if err == nil {
			return types.BytesToAddress(b)
		}
	}

	fatal("invalid contract address: %s\n  Use a 40-char hex address (see 'tylcli contracts' to list deployed contracts).", ref)
	return types.ZeroAddress
}

// ============================================================
// Transaction helpers
// ============================================================

func fetchNonce(addr types.Address) uint64 {
	result := rpcCall("tyl_getAccount", []string{addr.Hex()})
	var acct struct {
		Nonce uint64 `json:"nonce"`
	}
	json.Unmarshal(result, &acct)
	return acct.Nonce
}

func signTx(tx *types.Transaction, key *crypto.PrivateKey) {
	sigHash := tx.SigningHash()
	sig, err := crypto.Sign(sigHash, key)
	if err != nil {
		fatal("signing error: %v", err)
	}
	tx.Signature = sig
}

// ============================================================
// Swap commands (high-level atomic swap UX)
// ============================================================

func cmdSwap(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli swap <buy|sell|list|status> [args...]")
	}
	switch args[0] {
	case "buy":
		cmdSwapBuy(args[1:])
	case "sell":
		cmdSwapSell(args[1:])
	case "list":
		cmdSwapList()
	case "status":
		cmdSwapStatus(args[1:])
	default:
		fatal("unknown swap subcommand: %s\n\nSubcommands: buy, sell, list, status", args[0])
	}
}

// swap buy --amount <amt> [--from <key>]
// End-to-end buy flow:
//  1. Generate preimage + hash, save state to disk
//  2. Call swap daemon → daemon locks HTLC + creates LN invoice
//  3. Print invoice for user to pay
//  4. Poll until HTLC appears on-chain
//  5. Auto-claim the HTLC
//
// Restart-safe: if interrupted, re-run with --resume <htlc_id> to continue.
func cmdSwapBuy(args []string) {
	_, flags := parseFlags(args)

	// Resume an interrupted buy?
	if resumeStr, ok := flags["resume"]; ok && resumeStr != "" {
		cmdSwapBuyResume(mustParseUint(resumeStr, "resume"), flags)
		return
	}

	amountStr, ok := flags["amount"]
	if !ok || amountStr == "" {
		fatal("usage: tylcli swap buy --amount <tyBTC amount>\n       tylcli swap buy --resume <htlc_id>")
	}
	amount := mustParseUint(amountStr, "amount")

	// Resolve the user's signing key and address.
	key := resolveSigningKey(flags["from"])
	addr := key.Public().Address()

	// Generate preimage and hash.
	preimage, err := crypto.GeneratePreimage()
	if err != nil {
		fatal("generate preimage: %v", err)
	}
	hash := types.Sha256(preimage[:])

	// Call the swap daemon.
	reqBody, _ := json.Marshal(map[string]interface{}{
		"hash":          hex.EncodeToString(hash[:]),
		"recipientAddr": addr.Hex(),
		"amountTyBTC":   amount,
	})

	resp := swapdPost("/swap/buy", reqBody)
	var buyResp struct {
		SwapID  string `json:"swapID"`
		Invoice string `json:"invoice"`
		HTLCID  uint64 `json:"htlcID"`
	}
	if err := json.Unmarshal(resp, &buyResp); err != nil {
		fatal("parse swap response: %v", err)
	}

	// Persist preimage to disk BEFORE printing invoice — crash-safe.
	saveSwapBuyState(buyResp.HTLCID, preimage, buyResp.Invoice)

	fmt.Printf("swap %s: HTLC %d, %d tyBTC\n\n", buyResp.SwapID, buyResp.HTLCID, amount)
	fmt.Println("Pay this Lightning invoice:")
	fmt.Println(buyResp.Invoice)
	fmt.Println()

	// If interrupted, the user can resume with: tylcli swap buy --resume <htlc_id>
	swapBuyWaitAndClaim(buyResp.HTLCID, preimage, key)
}

// cmdSwapBuyResume picks up a buy swap that was interrupted.
func cmdSwapBuyResume(htlcID uint64, flags map[string]string) {
	state := loadSwapBuyState(htlcID)
	if state == nil {
		fatal("no saved state for HTLC %d\n  Only HTLCs created via 'tylcli swap buy' can be resumed.", htlcID)
	}

	key := resolveSigningKey(flags["from"])
	fmt.Printf("Resuming buy swap for HTLC %d\n", htlcID)

	// Check if already claimed.
	base := uint64(10000) + htlcID*14
	sysAddr := executor.HTLCSystemAddress.Hex()
	status := readStorageUint64Safe(sysAddr, base+8)
	if status == 1 {
		fmt.Println("HTLC already claimed!")
		return
	}
	if status == 2 {
		fmt.Println("HTLC was refunded.")
		return
	}

	swapBuyWaitAndClaim(htlcID, state.Preimage, key)
}

func swapBuyWaitAndClaim(htlcID uint64, preimage [32]byte, key *crypto.PrivateKey) {
	addr := key.Public().Address()
	base := uint64(10000) + htlcID*14
	sysAddr := executor.HTLCSystemAddress.Hex()

	fmt.Println("Waiting for HTLC to appear on-chain...")

	for {
		htlcAmount := readStorageUint64Safe(sysAddr, base+2)
		if htlcAmount > 0 {
			break
		}
		time.Sleep(3 * time.Second)
		fmt.Print(".")
	}
	fmt.Println()

	// Check it's still pending (not refunded while we waited).
	status := readStorageUint64Safe(sysAddr, base+8)
	if status != 0 {
		if status == 1 {
			fmt.Println("HTLC already claimed!")
		} else {
			fmt.Println("HTLC was refunded.")
		}
		return
	}

	// Verify hashlock matches our preimage before claiming (guards against HTLC ID shift).
	expectedHash := types.Sha256(preimage[:])
	hw0 := readStorageUint64Safe(sysAddr, base+3)
	hw1 := readStorageUint64Safe(sysAddr, base+4)
	hw2 := readStorageUint64Safe(sysAddr, base+5)
	hw3 := readStorageUint64Safe(sysAddr, base+6)
	var onChainHash [32]byte
	beWriteUint64(onChainHash[0:8], hw0)
	beWriteUint64(onChainHash[8:16], hw1)
	beWriteUint64(onChainHash[16:24], hw2)
	beWriteUint64(onChainHash[24:32], hw3)
	if types.Hash256(expectedHash) != types.Hash256(onChainHash) {
		fatal("HTLC %d hashlock mismatch — the HTLC ID may have shifted.\n  Expected: %s\n  On-chain: %s\n  Use 'tylcli htlc query' to find the correct HTLC.", htlcID,
			hex.EncodeToString(expectedHash[:]), hex.EncodeToString(onChainHash[:]))
	}

	fmt.Println("HTLC locked on-chain. Claiming...")

	// Prevent duplicate claim submissions.
	checkHTLCPending(htlcID, "claim")

	// Build and submit the claim transaction.
	preimageBytes := preimage[:]
	pw0 := beUint64(preimageBytes[0:8])
	pw1 := beUint64(preimageBytes[8:16])
	pw2 := beUint64(preimageBytes[16:24])
	pw3 := beUint64(preimageBytes[24:32])

	data := contracts.PackCallData(executor.HTLCFnClaim, htlcID, pw0, pw1, pw2, pw3)

	nonce := fetchNonce(addr)
	tx := &types.Transaction{
		Version: 1, Nonce: nonce, From: addr,
		To: executor.HTLCSystemAddress,
		GasPrice: 0, GasLimit: 0, Data: data,
	}
	signTx(tx, key)

	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))
	txid := tx.TxID().Hex()

	pushAndReport(envelope, 0, txid)
	markHTLCPending(htlcID, "claim", txid)
	fmt.Println()
	fmt.Println("Claim submitted. Your tyBTC will arrive once the L1 tx is mined.")
}

// --- Swap buy state persistence ---

type swapBuyState struct {
	Preimage [32]byte `json:"preimage"`
	Invoice  string   `json:"invoice"`
}

func swapBuyStatePath(htlcID uint64) string {
	return filepath.Join(pendingDir, "swaps", fmt.Sprintf("buy_%d.json", htlcID))
}

func saveSwapBuyState(htlcID uint64, preimage [32]byte, invoice string) {
	dir := filepath.Join(pendingDir, "swaps")
	os.MkdirAll(dir, 0700)
	state := swapBuyState{Preimage: preimage, Invoice: invoice}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(swapBuyStatePath(htlcID), data, 0600); err != nil {
		// This is critical — if we can't save the preimage, the user could lose funds.
		fatal("CRITICAL: failed to save swap state: %v\n  Preimage: %s\n  Save this preimage manually!", err, hex.EncodeToString(preimage[:]))
	}
}

func loadSwapBuyState(htlcID uint64) *swapBuyState {
	data, err := os.ReadFile(swapBuyStatePath(htlcID))
	if err != nil {
		return nil
	}
	var state swapBuyState
	if json.Unmarshal(data, &state) != nil {
		return nil
	}
	return &state
}

// readStorageUint64Safe is like readStorageUint64 but returns 0 on any error.
func readStorageUint64Safe(addrHex string, slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	result := rpcCallSafe("tyl_getStorage", []string{addrHex, key.Hex()})
	if result == nil {
		return 0
	}
	var sr struct {
		Value string `json:"value"`
	}
	json.Unmarshal(result, &sr)
	valBytes, _ := hex.DecodeString(stripHex(sr.Value))
	if len(valBytes) < 32 {
		return 0
	}
	return binary.BigEndian.Uint64(valBytes[24:])
}

// swap sell --htlc <id> --invoice <bolt11>
// Tells the swap daemon to pay the user's invoice and claim the HTLC.
func cmdSwapSell(args []string) {
	_, flags := parseFlags(args)

	htlcStr, ok := flags["htlc"]
	if !ok || htlcStr == "" {
		fatal("usage: tylcli swap sell --htlc <id> --invoice <bolt11>")
	}
	htlcID := mustParseUint(htlcStr, "htlc")

	invoice, ok := flags["invoice"]
	if !ok || invoice == "" {
		fatal("usage: tylcli swap sell --htlc <id> --invoice <bolt11>")
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"htlcID":  htlcID,
		"invoice": invoice,
	})

	resp := swapdPost("/swap/sell", reqBody)
	var sellResp struct {
		SwapID string `json:"swapID"`
	}
	if err := json.Unmarshal(resp, &sellResp); err != nil {
		fatal("parse swap response: %v", err)
	}

	fmt.Printf("swap %s created\n", sellResp.SwapID)
	fmt.Printf("  HTLC:    %d\n", htlcID)
	fmt.Printf("  invoice: %s\n", invoice)
	fmt.Println()
	fmt.Println("The daemon will pay your invoice and claim the HTLC.")
	fmt.Printf("Track with: tylcli swap status %s\n", sellResp.SwapID)
}

func cmdSwapList() {
	resp := swapdGet("/swap/list")
	var swaps []struct {
		ID          string `json:"id"`
		Direction   int    `json:"direction"`
		State       int    `json:"state"`
		AmountTyBTC uint64 `json:"amountTyBTC"`
		HTLCID      uint64 `json:"htlcID"`
		Error       string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &swaps); err != nil {
		fatal("parse swap list: %v", err)
	}

	if len(swaps) == 0 {
		fmt.Println("no swaps")
		return
	}

	for _, s := range swaps {
		dir := "buy "
		if s.Direction == 2 {
			dir = "sell"
		}
		state := swapStateName(s.State)
		errSuffix := ""
		if s.Error != "" {
			errSuffix = "  err: " + s.Error
		}
		fmt.Printf("%-8s %s  %-18s  htlc:%-4d  %d tyBTC%s\n", s.ID, dir, state, s.HTLCID, s.AmountTyBTC, errSuffix)
	}
}

func cmdSwapStatus(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli swap status <swap_id>")
	}
	swapID := args[0]
	resp := swapdGet("/swap/status?id=" + swapID)
	var s struct {
		ID          string `json:"id"`
		Direction   int    `json:"direction"`
		State       int    `json:"state"`
		AmountTyBTC uint64 `json:"amountTyBTC"`
		AmountSats  int64  `json:"amountSats"`
		HTLCID      uint64 `json:"htlcID"`
		Timelock    uint64 `json:"timelock"`
		Invoice     string `json:"invoice,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &s); err != nil {
		fatal("parse swap status: %v", err)
	}

	dir := "buy"
	if s.Direction == 2 {
		dir = "sell"
	}
	fmt.Printf("swap %s (%s):\n", s.ID, dir)
	fmt.Printf("  state:    %s\n", swapStateName(s.State))
	fmt.Printf("  amount:   %d tyBTC (%d sats)\n", s.AmountTyBTC, s.AmountSats)
	fmt.Printf("  htlc:     %d\n", s.HTLCID)
	fmt.Printf("  timelock: %d\n", s.Timelock)
	if s.Invoice != "" {
		fmt.Printf("  invoice:  %s\n", s.Invoice)
	}
	if s.Error != "" {
		fmt.Printf("  error:    %s\n", s.Error)
	}
}

func swapStateName(state int) string {
	switch state {
	case 0:
		return "created"
	case 1:
		return "htlc_locked"
	case 2:
		return "invoice_created"
	case 3:
		return "payment_in_flight"
	case 4:
		return "claimed"
	case 5:
		return "settled"
	case 6:
		return "refunded"
	case 7:
		return "failed"
	case 8:
		return "refund_submitted"
	default:
		return fmt.Sprintf("unknown(%d)", state)
	}
}

// swapdPost sends a POST request to the swap daemon.
func swapdPost(path string, body []byte) json.RawMessage {
	req, err := http.NewRequest("POST", swapdEndpoint+path, bytes.NewReader(body))
	if err != nil {
		fatal("swap daemon request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if swapdAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+swapdAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("swap daemon unreachable: %v\n  Is tylswapd running at %s?", err, swapdEndpoint)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Try to extract error message.
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			fatal("swap daemon: %s", errResp.Error)
		}
		fatal("swap daemon: HTTP %d: %s", resp.StatusCode, data)
	}
	return json.RawMessage(data)
}

// swapdGet sends a GET request to the swap daemon.
func swapdGet(path string) json.RawMessage {
	req, err := http.NewRequest("GET", swapdEndpoint+path, nil)
	if err != nil {
		fatal("swap daemon request: %v", err)
	}
	if swapdAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+swapdAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("swap daemon unreachable: %v\n  Is tylswapd running at %s?", err, swapdEndpoint)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			fatal("swap daemon: %s", errResp.Error)
		}
		fatal("swap daemon: HTTP %d: %s", resp.StatusCode, data)
	}
	return json.RawMessage(data)
}

// ============================================================
// HTLC commands
// ============================================================

func cmdHTLC(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli htlc <lock|claim|refund|query> [args...]")
	}
	switch args[0] {
	case "lock":
		cmdHTLCLock(args[1:])
	case "claim":
		cmdHTLCClaim(args[1:])
	case "refund":
		cmdHTLCRefund(args[1:])
	case "query":
		cmdHTLCQuery(args[1:])
	default:
		fatal("unknown htlc subcommand: %s\n\nSubcommands: lock, claim, refund, query", args[0])
	}
}

// htlc lock <to> <timelock> <hash> --value <amount> [--from <key>]
func cmdHTLCLock(args []string) {
	pos, flags := parseFlags(args)
	if len(pos) < 3 {
		fatal("usage: tylcli htlc lock <to> <timelock> <hash> --value <amount>")
	}

	recipientID := resolveArg(pos[0])
	timelock := mustParseUint(pos[1], "timelock")
	hashHex := stripHex(pos[2])
	hashBytes, err := hex.DecodeString(hashHex)
	if err != nil || len(hashBytes) != 32 {
		fatal("hash must be 32-byte hex (64 chars)")
	}

	hw0 := beUint64(hashBytes[0:8])
	hw1 := beUint64(hashBytes[8:16])
	hw2 := beUint64(hashBytes[16:24])
	hw3 := beUint64(hashBytes[24:32])

	data := contracts.PackCallData(executor.HTLCFnLock, recipientID, timelock, hw0, hw1, hw2, hw3)

	value, ok := flags["value"]
	if !ok || value == "" {
		fatal("--value is required for htlc lock")
	}
	amount := mustParseUint(value, "value")

	key := resolveSigningKey(flags["from"])
	addr := key.Public().Address()
	nonce := fetchNonce(addr)

	tx := &types.Transaction{
		Version: 1, Nonce: nonce, From: addr,
		To: executor.HTLCSystemAddress, Value: amount,
		GasPrice: 1, GasLimit: 100_000, Data: data,
	}
	signTx(tx, key)

	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))
	txid := tx.TxID().Hex()

	fmt.Printf("htlc lock: %d tyBTC, timelock=%d\n", amount, timelock)
	if flags["no-push"] == "" {
		pushAndReport(envelope, 0, txid)
	} else {
		fmt.Printf("txid: %s\nenvelope: %s\n", txid, hex.EncodeToString(envelope))
	}
}

// htlc claim <htlc_id> <preimage> [--from <key>]
func cmdHTLCClaim(args []string) {
	pos, flags := parseFlags(args)
	if len(pos) < 2 {
		fatal("usage: tylcli htlc claim <htlc_id> <preimage>")
	}

	htlcID := mustParseUint(pos[0], "htlc_id")

	// Pre-check: reject if HTLC is not pending on-chain.
	base := uint64(10000) + htlcID*14
	sysAddr := executor.HTLCSystemAddress.Hex()
	status := readStorageUint64(sysAddr, base+8)
	if status != 0 {
		statusStr := "unknown"
		switch status {
		case 1:
			statusStr = "already claimed"
		case 2:
			statusStr = "already refunded"
		}
		fatal("HTLC %d is not pending (status: %s)", htlcID, statusStr)
	}

	// Pre-check: reject if a claim is already in-flight.
	checkHTLCPending(htlcID, "claim")

	preimageHex := stripHex(pos[1])
	preimageBytes, err := hex.DecodeString(preimageHex)
	if err != nil || len(preimageBytes) != 32 {
		fatal("preimage must be 32-byte hex (64 chars)")
	}

	pw0 := beUint64(preimageBytes[0:8])
	pw1 := beUint64(preimageBytes[8:16])
	pw2 := beUint64(preimageBytes[16:24])
	pw3 := beUint64(preimageBytes[24:32])

	data := contracts.PackCallData(executor.HTLCFnClaim, htlcID, pw0, pw1, pw2, pw3)

	key := resolveSigningKey(flags["from"])
	addr := key.Public().Address()
	nonce := fetchNonce(addr)

	tx := &types.Transaction{
		Version: 1, Nonce: nonce, From: addr,
		To: executor.HTLCSystemAddress,
		GasPrice: 0, GasLimit: 0, Data: data,
	}
	signTx(tx, key)

	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))
	txid := tx.TxID().Hex()

	fmt.Printf("htlc claim: id=%d\n", htlcID)
	if flags["no-push"] == "" {
		pushAndReport(envelope, 0, txid)
		markHTLCPending(htlcID, "claim", txid)
	} else {
		fmt.Printf("txid: %s\nenvelope: %s\n", txid, hex.EncodeToString(envelope))
	}
}

// htlc refund <htlc_id> [--from <key>]
func cmdHTLCRefund(args []string) {
	pos, flags := parseFlags(args)
	if len(pos) < 1 {
		fatal("usage: tylcli htlc refund <htlc_id>")
	}

	htlcID := mustParseUint(pos[0], "htlc_id")

	// Pre-check: reject if HTLC is not pending on-chain.
	base := uint64(10000) + htlcID*14
	sysAddr := executor.HTLCSystemAddress.Hex()
	status := readStorageUint64(sysAddr, base+8)
	if status != 0 {
		statusStr := "unknown"
		switch status {
		case 1:
			statusStr = "already claimed"
		case 2:
			statusStr = "already refunded"
		}
		fatal("HTLC %d is not pending (status: %s)", htlcID, statusStr)
	}

	// Pre-check: reject if a refund is already in-flight.
	checkHTLCPending(htlcID, "refund")

	data := contracts.PackCallData(executor.HTLCFnRefund, htlcID)

	key := resolveSigningKey(flags["from"])
	addr := key.Public().Address()
	nonce := fetchNonce(addr)

	tx := &types.Transaction{
		Version: 1, Nonce: nonce, From: addr,
		To: executor.HTLCSystemAddress,
		GasPrice: 1, GasLimit: 100_000, Data: data,
	}
	signTx(tx, key)

	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))
	txid := tx.TxID().Hex()

	fmt.Printf("htlc refund: id=%d\n", htlcID)
	if flags["no-push"] == "" {
		pushAndReport(envelope, 0, txid)
		markHTLCPending(htlcID, "refund", txid)
	} else {
		fmt.Printf("txid: %s\nenvelope: %s\n", txid, hex.EncodeToString(envelope))
	}
}

// htlc query <htlc_id>
func cmdHTLCQuery(args []string) {
	pos, _ := parseFlags(args)
	if len(pos) < 1 {
		fatal("usage: tylcli htlc query <htlc_id>")
	}

	htlcID := mustParseUint(pos[0], "htlc_id")
	base := 10000 + htlcID*14
	sysAddr := executor.HTLCSystemAddress.Hex()

	senderID := readStorageUint64(sysAddr, base+0)
	recipientID := readStorageUint64(sysAddr, base+1)
	amount := readStorageUint64(sysAddr, base+2)
	timelock := readStorageUint64(sysAddr, base+7)
	status := readStorageUint64(sysAddr, base+8)

	// Read hashlock.
	hw0 := readStorageUint64(sysAddr, base+3)
	hw1 := readStorageUint64(sysAddr, base+4)
	hw2 := readStorageUint64(sysAddr, base+5)
	hw3 := readStorageUint64(sysAddr, base+6)
	var hashlock [32]byte
	beWriteUint64(hashlock[0:8], hw0)
	beWriteUint64(hashlock[8:16], hw1)
	beWriteUint64(hashlock[16:24], hw2)
	beWriteUint64(hashlock[24:32], hw3)

	statusStr := "unknown"
	switch status {
	case 0:
		statusStr = "pending"
	case 1:
		statusStr = "claimed"
	case 2:
		statusStr = "refunded"
	}

	fmt.Printf("HTLC %d:\n", htlcID)
	fmt.Printf("  status:    %s (%d)\n", statusStr, status)
	fmt.Printf("  sender:    %d\n", senderID)
	fmt.Printf("  recipient: %d\n", recipientID)
	fmt.Printf("  amount:    %d\n", amount)
	fmt.Printf("  timelock:  %d\n", timelock)
	fmt.Printf("  hashlock:  %s\n", hex.EncodeToString(hashlock[:]))

	if status == 1 {
		pw0 := readStorageUint64(sysAddr, base+9)
		pw1 := readStorageUint64(sysAddr, base+10)
		pw2 := readStorageUint64(sysAddr, base+11)
		pw3 := readStorageUint64(sysAddr, base+12)
		var preimage [32]byte
		beWriteUint64(preimage[0:8], pw0)
		beWriteUint64(preimage[8:16], pw1)
		beWriteUint64(preimage[16:24], pw2)
		beWriteUint64(preimage[24:32], pw3)
		fmt.Printf("  preimage:  %s\n", hex.EncodeToString(preimage[:]))
	}
}

func readStorageUint64(addrHex string, slot uint64) uint64 {
	var key types.Hash256
	binary.BigEndian.PutUint64(key[24:], slot)
	result := rpcCall("tyl_getStorage", []string{addrHex, key.Hex()})
	var sr struct {
		Value string `json:"value"`
	}
	json.Unmarshal(result, &sr)
	valBytes, _ := hex.DecodeString(stripHex(sr.Value))
	if len(valBytes) < 32 {
		return 0
	}
	return binary.BigEndian.Uint64(valBytes[24:])
}

func beUint64(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}

func beWriteUint64(b []byte, v uint64) {
	binary.BigEndian.PutUint64(b, v)
}

// ============================================================
// Raw tx builder (power user, backward compat)
// ============================================================

func cmdTx(args []string) {
	tx := &types.Transaction{Version: 1}
	var fromRef string
	nonceSet := false

	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) && strings.HasPrefix(args[i], "--") {
			fatal("flag %s requires a value", args[i])
		}
		switch args[i] {
		case "--to":
			i++
			b, err := hex.DecodeString(stripHex(args[i]))
			if err != nil || len(b) != 20 {
				fatal("invalid address: %s", args[i])
			}
			tx.To = types.BytesToAddress(b)
		case "--value":
			i++
			tx.Value = mustParseUint(args[i], "value")
		case "--nonce":
			i++
			tx.Nonce = mustParseUint(args[i], "nonce")
			nonceSet = true
		case "--gasprice":
			i++
			tx.GasPrice = mustParseUint(args[i], "gasprice")
		case "--gaslimit":
			i++
			tx.GasLimit = mustParseUint(args[i], "gaslimit")
		case "--priority":
			i++
			v := mustParseUint(args[i], "priority")
			tx.Priority = uint8(v)
		case "--data":
			i++
			d, err := hex.DecodeString(stripHex(args[i]))
			if err != nil {
				fatal("invalid data hex: %v", err)
			}
			tx.Data = d
		case "--from":
			i++
			fromRef = args[i]
		default:
			fatal("unknown flag: %s", args[i])
		}
	}

	if tx.GasLimit == 0 {
		tx.GasLimit = 21000
	}
	if tx.GasPrice == 0 {
		tx.GasPrice = 1
	}

	privKey := resolveSigningKey(fromRef)
	tx.From = privKey.Public().Address()

	if !nonceSet {
		tx.Nonce = fetchNonce(tx.From)
	}

	signTx(tx, privKey)

	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, encoding.EncodeTx(tx))

	fmt.Printf("txid:     %s\n", tx.TxID().Hex())
	fmt.Printf("from:     %s\n", tx.From.Hex())
	if tx.To.IsZero() && len(tx.Data) > 0 {
		contractAddr := executor.DeriveContractAddress(tx.From, tx.Nonce)
		fmt.Printf("contract: %s\n", contractAddr.Hex())
	}
	fmt.Printf("envelope: %s\n", hex.EncodeToString(envelope))
}

// pushAndReport pushes an envelope, waits for the Tylium block, and prints
// the txid. Use `tylcli status <txid>` to check the result.
// burnSats is the BTC to burn in the OP_RETURN output (for deposits).
// pushAndReport broadcasts an envelope and tracks its txid.
// If txid is empty, it's computed after broadcast (used for deposits that
// need the L1 txid to derive a unique nonce).
func pushAndReport(envelope []byte, burnSats uint64, txid string) {
	l1txid := push(envelope, burnSats)
	if txid == "" {
		// Deposit: derive nonce from L1 txid, then compute Tylium txid.
		txid = depositTxIDFromL1(envelope, l1txid)
	}
	trackPending(txid)
	fmt.Printf("l1 txid: %s\n", l1txid)
	fmt.Printf("   txid: %s\n", txid)
	fmt.Println("use 'tylcli status <txid>' to check once included in a block")
}

// depositTxIDFromL1 computes the Tylium txid for a deposit using the L1 txid
// to derive a unique nonce, matching what the node's extractor does.
func depositTxIDFromL1(envelope []byte, l1txidHex string) string {
	// Decode the deposit envelope to get recipient and amount.
	env, err := encoding.DecodeEnvelope(envelope)
	if err != nil {
		fatal("decode envelope: %v", err)
	}
	recipient, amount, err := encoding.DecodeDeposit(env.Payload)
	if err != nil {
		fatal("decode deposit: %v", err)
	}

	// Derive nonce from L1 txid (first 8 bytes), matching scanner.btcTxNonce.
	l1bytes, _ := hex.DecodeString(l1txidHex)
	l1hash := types.BytesToHash256(l1bytes)
	nonce := uint64(l1hash[0])<<56 | uint64(l1hash[1])<<48 | uint64(l1hash[2])<<40 | uint64(l1hash[3])<<32 |
		uint64(l1hash[4])<<24 | uint64(l1hash[5])<<16 | uint64(l1hash[6])<<8 | uint64(l1hash[7])

	tx := &types.Transaction{Version: 1, Nonce: nonce, To: recipient, Value: amount}
	return tx.TxID().Hex()
}

func getHeight() uint64 {
	result := rpcCallSafe("tyl_blockHeight", nil)
	if result == nil {
		return 0
	}
	var hr struct {
		Height uint64 `json:"height"`
	}
	json.Unmarshal(result, &hr)
	return hr.Height
}

func getFinalizedHeight() uint64 {
	result := rpcCallSafe("tyl_finalizedHeight", nil)
	if result == nil {
		return 0
	}
	var fr struct {
		FinalizedHeight uint64 `json:"finalizedHeight"`
	}
	json.Unmarshal(result, &fr)
	return fr.FinalizedHeight
}

type receiptInfo struct {
	Success bool   `json:"Success"`
	GasUsed uint64 `json:"GasUsed"`
	Err     string `json:"Err"`
}

func getReceipts(height uint64) []receiptInfo {
	result := rpcCallSafe("tyl_getReceipts", []uint64{height})
	if result == nil {
		return nil
	}
	var receipts []receiptInfo
	json.Unmarshal(result, &receipts)
	return receipts
}

// ============================================================
// Query commands
// ============================================================

func cmdHeight() {
	result := rpcCall("tyl_blockHeight", nil)
	var hr struct {
		Height uint64 `json:"height"`
	}
	json.Unmarshal(result, &hr)
	fmt.Println(hr.Height)
}

func cmdFinalized() {
	result := rpcCall("tyl_finalizedHeight", nil)
	var fr struct {
		FinalizedHeight  uint64 `json:"finalizedHeight"`
		FinalizedRoot    string `json:"finalizedRoot"`
		OptimisticHeight uint64 `json:"optimisticHeight"`
	}
	json.Unmarshal(result, &fr)
	fmt.Printf("finalized:  %d\n", fr.FinalizedHeight)
	fmt.Printf("optimistic: %d\n", fr.OptimisticHeight)
	if fr.FinalizedHeight > 0 {
		fmt.Printf("state root: %s\n", fr.FinalizedRoot)
	}
	gap := fr.OptimisticHeight - fr.FinalizedHeight
	if gap > 0 {
		fmt.Printf("gap:        %d blocks unattested\n", gap)
	}
}

func cmdStateRoot() {
	result := rpcCall("tyl_stateRoot", nil)
	prettyPrint(result)
}

func cmdAccount(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli account <address|name>")
	}
	addr := resolveAddress(args[0])
	result := rpcCall("tyl_getAccount", []string{addr})
	var acct struct {
		Nonce      uint64 `json:"nonce"`
		Balance    uint64 `json:"balance"`
		IsContract bool   `json:"isContract"`
	}
	json.Unmarshal(result, &acct)
	fmt.Printf("address:  %s\n", addr)
	fmt.Printf("balance:  %d\n", acct.Balance)
	fmt.Printf("nonce:    %d\n", acct.Nonce)
	if acct.IsContract {
		fmt.Printf("contract: true\n")
	}
}

func cmdBalance(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli balance <address|name>")
	}
	addr := resolveAddress(args[0])
	result := rpcCall("tyl_getBalance", []string{addr})
	var br struct {
		Balance          uint64  `json:"balance"`
		FinalizedBalance *uint64 `json:"finalizedBalance"`
	}
	json.Unmarshal(result, &br)

	if br.FinalizedBalance != nil && *br.FinalizedBalance == br.Balance {
		fmt.Printf("tyBTC: %d  [finalized]\n", br.Balance)
	} else if br.FinalizedBalance != nil {
		fmt.Printf("tyBTC: %d  [unfinalized]\n", br.Balance)
		fmt.Printf("  confirmed: %d\n", *br.FinalizedBalance)
		diff := int64(br.Balance) - int64(*br.FinalizedBalance)
		if diff > 0 {
			fmt.Printf("  pending:   +%d\n", diff)
		} else if diff < 0 {
			fmt.Printf("  pending:   %d\n", diff)
		}
	} else {
		fmt.Printf("tyBTC: %d  [no attestations yet]\n", br.Balance)
	}

	// Show token balances and LP positions from all saved contracts.
	callerID := addrToCallerID(addr)
	seen := make(map[string]bool) // dedupe by address
	for _, sc := range listContracts() {
		cleanAddr := strings.ToLower(stripHex(sc.Address))
		if seen[cleanAddr] {
			continue
		}
		seen[cleanAddr] = true

		switch sc.Type {
		case "token":
			r := queryStorageSlotFull(sc.Address, uint64(contracts.SlotBalanceBase)+callerID)
			if r.Value > 0 || (r.Finalized != nil && *r.Finalized > 0) {
				name := sc.Name
				if name == "" {
					name = sc.Address
				}
				if r.Finalized != nil && *r.Finalized == r.Value {
					fmt.Printf("%s: %d  [finalized]\n", name, r.Value)
				} else if r.Finalized != nil {
					fmt.Printf("%s: %d  [unfinalized, confirmed: %d]\n", name, r.Value, *r.Finalized)
				} else {
					fmt.Printf("%s: %d  [no attestations yet]\n", name, r.Value)
				}
			}
		case "amm":
			lpR := queryStorageSlotFull(sc.Address, uint64(contracts.AMMSlotLPBalBase)+callerID)
			if lpR.Value > 0 {
				lpSupply := queryStorageSlot(sc.Address, uint64(contracts.AMMSlotLPSupply))
				resA := queryStorageSlot(sc.Address, uint64(contracts.AMMSlotReserveA))
				resB := queryStorageSlot(sc.Address, uint64(contracts.AMMSlotReserveB))
				name := sc.Name
				if name == "" {
					name = sc.Address[:12] + "..."
				}
				pct := float64(lpR.Value) / float64(lpSupply) * 100
				if lpSupply > 0 && lpR.Value < lpSupply {
					fmt.Printf("LP %s: %d LP (%.1f%% of pool)", name, lpR.Value, pct)
					shareA := resA * lpR.Value / lpSupply
					shareB := resB * lpR.Value / lpSupply
					fmt.Printf("  ≈ %d tokenA + %d tyBTC", shareA, shareB)
				} else if lpSupply > 0 {
					fmt.Printf("LP %s: %d LP (sole provider)", name, lpR.Value)
					fmt.Printf("  ≈ %d tokenA + %d tyBTC", resA, resB)
				} else {
					fmt.Printf("LP %s: %d LP", name, lpR.Value)
				}
				if lpR.Finalized != nil && *lpR.Finalized == lpR.Value {
					fmt.Printf("  [finalized]\n")
				} else if lpR.Finalized != nil {
					fmt.Printf("  [unfinalized]\n")
				} else {
					fmt.Printf("  [no attestations yet]\n")
				}
			}
		}
	}
}

type slotResult struct {
	Value     uint64
	Finalized *uint64 // nil if no finalized snapshot
}

func queryStorageSlot(contractAddr string, slot uint64) uint64 {
	r := queryStorageSlotFull(contractAddr, slot)
	return r.Value
}

func queryStorageSlotFull(contractAddr string, slot uint64) slotResult {
	slotHex := fmt.Sprintf("%064x", slot)
	storageResult := rpcCallSafe("tyl_getStorage", []string{contractAddr, slotHex})
	if storageResult == nil {
		return slotResult{}
	}
	var sr struct {
		Value          string  `json:"value"`
		FinalizedValue *string `json:"finalizedValue"`
	}
	json.Unmarshal(storageResult, &sr)

	result := slotResult{Value: parseHash256Uint64(sr.Value)}
	if sr.FinalizedValue != nil {
		fv := parseHash256Uint64(*sr.FinalizedValue)
		result.Finalized = &fv
	}
	return result
}

func parseHash256Uint64(hexStr string) uint64 {
	valBytes, _ := hex.DecodeString(stripHex(hexStr))
	if len(valBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(valBytes):], valBytes)
		valBytes = padded
	}
	if len(valBytes) >= 32 {
		return binary.BigEndian.Uint64(valBytes[24:32])
	}
	return 0
}

// listContracts returns all saved contracts from the registry.
func listContracts() []savedContract {
	entries, _ := os.ReadDir(contractDir)
	var result []savedContract
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(contractDir, e.Name()))
		var sc savedContract
		if json.Unmarshal(data, &sc) == nil && sc.Address != "" {
			result = append(result, sc)
		}
	}
	return result
}

func cmdStorage(args []string) {
	if len(args) < 2 {
		fatal("usage: tylcli storage <address> <slot>")
	}
	slotArg := args[1]
	if n, err := strconv.ParseUint(slotArg, 0, 64); err == nil && len(slotArg) < 64 {
		slotArg = fmt.Sprintf("%064x", n)
	}
	result := rpcCall("tyl_getStorage", []string{args[0], slotArg})
	prettyPrint(result)
}

func cmdCode(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli code <address>")
	}
	result := rpcCall("tyl_getCode", []string{args[0]})
	prettyPrint(result)
}

func cmdBlock(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli block <height>")
	}
	height, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		fatal("invalid height: %v", err)
	}
	result := rpcCall("tyl_getBlock", []uint64{height})
	prettyPrint(result)
}

func cmdReceipts(args []string) {
	var height uint64
	if len(args) >= 1 {
		var err error
		height, err = strconv.ParseUint(args[0], 10, 64)
		if err != nil {
			fatal("invalid height: %v", err)
		}
	} else {
		// Default to latest block.
		height = getHeight()
	}

	receipts := getReceipts(height)
	finalized := getFinalizedHeight()
	status := "unfinalized"
	if finalized >= height {
		status = "finalized"
	}

	fmt.Printf("block %d  [%s]\n", height, status)
	if len(receipts) == 0 {
		fmt.Println("  no transactions")
		return
	}
	for i, r := range receipts {
		if r.Success {
			fmt.Printf("  tx %d: ok  gas: %d\n", i, r.GasUsed)
		} else {
			fmt.Printf("  tx %d: FAILED  gas: %d  err: %s\n", i, r.GasUsed, r.Err)
		}
	}
}

func cmdStatus(args []string) {
	if len(args) < 1 {
		fatal("usage: tylcli status <txid>")
	}
	txidHex := stripHex(args[0])

	result := rpcCall("tyl_getReceipt", []string{txidHex})
	if string(result) == "null" {
		fmt.Printf("txid %s: pending (not yet in a block)\n", txidHex)
		return
	}

	var r struct {
		TxID      string `json:"txid"`
		Height    uint64 `json:"height"`
		Success   bool   `json:"success"`
		GasUsed   uint64 `json:"gasUsed"`
		Err       string `json:"err"`
		Finalized bool   `json:"finalized"`
	}
	json.Unmarshal(result, &r)

	status := "unfinalized"
	if r.Finalized {
		status = "finalized"
	}

	if r.Success {
		fmt.Printf("ok  block: %d  gas: %d  [%s]\n", r.Height, r.GasUsed, status)
	} else {
		fmt.Printf("FAILED  block: %d  gas: %d  err: %s  [%s]\n", r.Height, r.GasUsed, r.Err, status)
	}
}

func cmdHistory(args []string) {
	_, flags := parseFlags(args)
	limit := 20
	if v, ok := flags["limit"]; ok {
		limit = int(mustParseUint(v, "limit"))
	}

	// Show pending (unconfirmed) transactions first.
	pending := loadPending()
	var stillPending []string
	confirmed := make(map[string]bool)
	for _, txid := range pending {
		receipt := lookupReceipt(txid)
		if receipt == nil {
			stillPending = append(stillPending, txid)
			fmt.Printf("      %-14s                  %-12s  %s\n", "pending", "[mempool]", txid)
		} else {
			confirmed[txid] = true
		}
	}
	// Prune confirmed txids from pending file.
	if len(confirmed) > 0 {
		savePending(stillPending)
	}

	// Show confirmed transactions from node.
	result := rpcCall("tyl_recentTxs", []int{0, limit})
	var txs []struct {
		TxID      string `json:"txid"`
		Height    uint64 `json:"height"`
		Success   bool   `json:"success"`
		GasUsed   uint64 `json:"gasUsed"`
		Err       string `json:"err"`
		Finalized bool   `json:"finalized"`
	}
	json.Unmarshal(result, &txs)

	if len(txs) == 0 && len(stillPending) == 0 {
		fmt.Println("no transactions yet")
		return
	}

	for _, tx := range txs {
		status := "unfinalized"
		if tx.Finalized {
			status = "finalized"
		}
		result := "ok"
		if !tx.Success {
			result = "FAIL"
		}
		fmt.Printf("%-4s  block %-6d  gas %-8d  %-12s  %s\n", result, tx.Height, tx.GasUsed, "["+status+"]", tx.TxID)
	}
}

// lookupReceipt returns a receipt if the txid has been confirmed, nil otherwise.
func lookupReceipt(txid string) *struct{} {
	result := rpcCallSafe("tyl_getReceipt", []string{txid})
	if result == nil || string(result) == "null" {
		return nil
	}
	return &struct{}{}
}

// ============================================================
// Helpers
// ============================================================

func resolveAddress(ref string) string {
	clean := stripHex(ref)
	if len(clean) == 40 {
		if _, err := hex.DecodeString(clean); err == nil {
			return clean
		}
	}
	// Try contract registry.
	if sc := loadContract(ref); sc != nil {
		return sc.Address
	}
	// Try keystore.
	for _, sk := range listKeys() {
		addr := strings.TrimPrefix(strings.ToLower(sk.Address), "0x")
		if strings.EqualFold(sk.Name, ref) || strings.HasPrefix(addr, strings.ToLower(clean)) {
			return sk.Address
		}
	}
	// If we get here, ref is not a valid hex address, contract, or key name.
	fatal("could not resolve '%s' to an address.\n  Use a 40-char hex address, key name, or contract name.\n  Run 'tylcli keys' or 'tylcli contracts' to list known names.", ref)
	return ref
}

func parseFlags(args []string) (positional []string, flags map[string]string) {
	flags = make(map[string]string)
	for i := 0; i < len(args); i++ {
		if args[i] == "--no-push" {
			flags["no-push"] = "true"
			continue
		}
		if strings.HasPrefix(args[i], "--") && i+1 < len(args) {
			key := args[i][2:]
			i++
			flags[key] = args[i]
			continue
		}
		positional = append(positional, args[i])
	}
	return
}

// resolveArg tries to interpret a call argument as:
//  1. A 20-byte hex address → caller_id
//  2. A key name (e.g. "bob") → resolve address → caller_id
//  3. A numeric value (decimal or hex)
func resolveArg(s string) uint64 {
	// Try as 20-byte hex address.
	clean := stripHex(s)
	if len(clean) == 40 {
		if _, err := hex.DecodeString(clean); err == nil {
			return addrToCallerID(clean)
		}
	}

	// Try key name (convenience shortcut for local keys).
	for _, sk := range listKeys() {
		if strings.EqualFold(sk.Name, s) {
			return addrToCallerID(sk.Address)
		}
	}

	// Fall back to numeric.
	return mustParseUint(s, "arg")
}

// addrToCallerID extracts the low 8 bytes of a 20-byte address as a uint64,
// matching the executor's caller_id derivation.
func addrToCallerID(addrHex string) uint64 {
	b, _ := hex.DecodeString(stripHex(addrHex))
	if len(b) != 20 {
		fatal("invalid address for caller_id: %s", addrHex)
	}
	id := uint64(0)
	for i := 12; i < 20; i++ {
		id = (id << 8) | uint64(b[i])
	}
	return id
}

func mustParseUint(s, label string) uint64 {
	// Support 0x-prefixed hex and bare hex strings.
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		if err != nil {
			fatal("invalid %s: %s", label, s)
		}
		return v
	}
	// Try decimal first, then hex fallback.
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		v, err = strconv.ParseUint(s, 16, 64)
		if err != nil {
			fatal("invalid %s: %s (expected decimal or hex)", label, s)
		}
	}
	return v
}

func formatArgs(args []string) string {
	return strings.Join(args, ", ")
}

func knownFnNames() string {
	names := make([]string, 0, len(fnSelectors))
	for n := range fnSelectors {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// --- Pending HTLC operation tracking ---

// checkHTLCPending checks if an HTLC operation is already pending.
// If a pending op exists and the tx is still unconfirmed, it blocks.
// If the tx has since been confirmed, it clears the pending marker.
func checkHTLCPending(htlcID uint64, op string) {
	path := filepath.Join(pendingDir, fmt.Sprintf("htlc_%d_%s", htlcID, op))
	data, err := os.ReadFile(path)
	if err != nil {
		return // no pending op
	}
	txid := strings.TrimSpace(string(data))
	if txid == "" {
		os.Remove(path)
		return
	}
	// Check if the tx has been confirmed.
	receipt := lookupReceipt(txid)
	if receipt != nil {
		os.Remove(path) // confirmed, clear pending
		return
	}
	fatal("HTLC %d %s already pending (txid: %s)\nuse 'tylcli status %s' to check", htlcID, op, txid, txid)
}

// markHTLCPending records a pending HTLC operation.
func markHTLCPending(htlcID uint64, op, txid string) {
	os.MkdirAll(pendingDir, 0700)
	path := filepath.Join(pendingDir, fmt.Sprintf("htlc_%d_%s", htlcID, op))
	os.WriteFile(path, []byte(txid+"\n"), 0644)
}

// --- Pending txid tracking ---

func pendingFilePath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".tylium", "pending.txt")
}

func trackPending(txid string) {
	path := pendingFilePath()
	os.MkdirAll(filepath.Dir(path), 0700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(txid + "\n")
}

func loadPending() []string {
	data, err := os.ReadFile(pendingFilePath())
	if err != nil {
		return nil
	}
	var txids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			txids = append(txids, line)
		}
	}
	return txids
}

func savePending(txids []string) {
	path := pendingFilePath()
	if len(txids) == 0 {
		os.Remove(path)
		return
	}
	os.WriteFile(path, []byte(strings.Join(txids, "\n")+"\n"), 0644)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- Tylium RPC ---

type rpcReq struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func rpcCall(method string, params interface{}) json.RawMessage {
	result := rpcCallSafe(method, params)
	if result == nil {
		fatal("rpc error: no result from %s\n  Is tyld running at %s?", method, rpcEndpoint)
	}
	return result
}

// rpcCallSafe is like rpcCall but returns nil instead of calling fatal on errors.
func rpcCallSafe(method string, params interface{}) json.RawMessage {
	if params == nil {
		params = []interface{}{}
	}
	reqBody, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	resp, err := http.Post(rpcEndpoint, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var r rpcResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	if r.Error != nil {
		return nil
	}
	return r.Result
}

func prettyPrint(data json.RawMessage) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		fmt.Println(string(data))
		return
	}
	fmt.Println(pretty.String())
}

func stripHex(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
