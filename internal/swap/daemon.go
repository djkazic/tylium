package swap

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/internal/lnd"
	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/types"
)

// MinSellTimelock is the minimum remaining blocks on an HTLC timelock for the
// daemon to accept a sell swap. This gives the daemon enough time to pay the
// Lightning invoice and claim the HTLC before the sender can refund.
const MinSellTimelock = 6

// Config holds swap daemon configuration.
type Config struct {
	TylRPC     string
	BtcRPC     string
	BtcUser    string
	BtcPass    string
	BtcWallet  string
	LNDRest    string
	LNDCert    string
	LNDMac     string
	PrivKey    *crypto.PrivateKey
	ListenAddr string
	DataDir    string // directory for persistent state (required)
	APIKey     string // API key for authentication (optional)
	FeeBPS      int    // fee in basis points (e.g., 50 = 0.5%)
	Timelock    uint64 // HTLC timelock in blocks (default: 144)
	MaxSwapSize uint64 // max swap size in tyBTC (0 = unlimited)
}

// Daemon orchestrates atomic swaps between Lightning and Tylium.
type Daemon struct {
	cfg     Config
	tyl     *TyliumRPC
	btc     *bitcoin.RPCClient
	lnd     *lnd.Client
	privKey *crypto.PrivateKey
	addr    types.Address

	mu    sync.Mutex
	swaps map[string]*Swap
	seq   int

	// activeHashes tracks hashes used by non-terminal swaps to prevent duplicates.
	activeHashes map[[32]byte]string // hash -> swapID
}

// New creates a swap daemon.
func New(cfg Config, lndClient *lnd.Client) *Daemon {
	btc := bitcoin.NewRPCClient(cfg.BtcRPC, cfg.BtcUser, cfg.BtcPass)
	btc.EnsureWallet(cfg.BtcWallet)

	d := &Daemon{
		cfg:          cfg,
		tyl:          NewTyliumRPC(cfg.TylRPC),
		btc:          btc,
		lnd:          lndClient,
		privKey:      cfg.PrivKey,
		addr:         cfg.PrivKey.Public().Address(),
		swaps:        make(map[string]*Swap),
		activeHashes: make(map[[32]byte]string),
	}

	// Restore persisted state.
	if cfg.DataDir != "" {
		d.loadSwaps()
	}

	return d
}

// Run starts the swap daemon: HTTP API + background monitor.
func (d *Daemon) Run(ctx context.Context) error {
	// Start the HTLC monitor.
	go d.monitor(ctx)

	// Start the HTTP API.
	mux := http.NewServeMux()
	mux.HandleFunc("/swap/buy", d.handleBuy)
	mux.HandleFunc("/swap/sell", d.handleSell)
	mux.HandleFunc("/swap/status", d.handleStatus)
	mux.HandleFunc("/swap/list", d.handleList)

	var handler http.Handler = mux
	if d.cfg.APIKey != "" {
		handler = d.authMiddleware(mux)
	}

	srv := &http.Server{Addr: d.cfg.ListenAddr, Handler: handler}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	log.Printf("swap API listening on %s", d.cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// authMiddleware rejects requests without a valid API key.
func (d *Daemon) authMiddleware(next http.Handler) http.Handler {
	expected := []byte("Bearer " + d.cfg.APIKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(key, expected) != 1 {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// === Buy flow: user pays Lightning, receives tyBTC ===

type BuyRequest struct {
	Hash          string `json:"hash"`          // SHA-256 hash (hex, user-generated)
	RecipientAddr string `json:"recipientAddr"` // Tylium address to receive tyBTC
	AmountTyBTC   uint64 `json:"amountTyBTC"`   // tyBTC amount desired
}

type BuyResponse struct {
	SwapID  string `json:"swapID"`
	Invoice string `json:"invoice"` // Lightning invoice to pay
	HTLCID  uint64 `json:"htlcID"`
	L1TxID  string `json:"l1TxID"` // Bitcoin txid that embeds the HTLC lock
}

func (d *Daemon) handleBuy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)

	var req BuyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	hashBytes, err := hex.DecodeString(req.Hash)
	if err != nil || len(hashBytes) != 32 {
		jsonError(w, "hash must be 32-byte hex", http.StatusBadRequest)
		return
	}
	var hash [32]byte
	copy(hash[:], hashBytes)

	addrBytes, err := hex.DecodeString(req.RecipientAddr)
	if err != nil || len(addrBytes) != 20 {
		jsonError(w, "recipientAddr must be 20-byte hex", http.StatusBadRequest)
		return
	}
	recipient := types.BytesToAddress(addrBytes)

	resp, err := d.initBuySwap(hash, recipient, req.AmountTyBTC)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonReply(w, resp)
}

func (d *Daemon) initBuySwap(hash [32]byte, recipient types.Address, amountTyBTC uint64) (*BuyResponse, error) {
	if amountTyBTC == 0 {
		return nil, fmt.Errorf("amount must be > 0")
	}
	if d.cfg.MaxSwapSize > 0 && amountTyBTC > d.cfg.MaxSwapSize {
		return nil, fmt.Errorf("amount %d exceeds max swap size %d", amountTyBTC, d.cfg.MaxSwapSize)
	}

	// Reserve hash atomically to prevent TOCTOU race.
	d.mu.Lock()
	if existingID, ok := d.activeHashes[hash]; ok {
		d.mu.Unlock()
		return nil, fmt.Errorf("hash already in use by swap %s", existingID)
	}
	d.activeHashes[hash] = "" // placeholder reservation
	d.mu.Unlock()

	unreserve := func() {
		d.mu.Lock()
		delete(d.activeHashes, hash)
		d.mu.Unlock()
	}

	// Check daemon balance.
	_, balance, err := d.tyl.GetAccount(d.addr)
	if err != nil {
		unreserve()
		return nil, fmt.Errorf("check balance: %w", err)
	}
	if balance < amountTyBTC {
		unreserve()
		return nil, fmt.Errorf("insufficient liquidity: have %d, need %d", balance, amountTyBTC)
	}

	// Calculate sats amount (1:1 + fee).
	amountSats := int64(amountTyBTC)
	if d.cfg.FeeBPS > 0 {
		amountSats = amountSats * int64(10000+d.cfg.FeeBPS) / 10000
	}

	// Get current height for timelock.
	height, err := d.tyl.GetBlockHeight()
	if err != nil {
		unreserve()
		return nil, fmt.Errorf("get height: %w", err)
	}
	timelock := height + d.cfg.Timelock

	// Generate swap ID early for invoice memo.
	swapID := d.newSwapID()

	// Create LND hold invoice first (reversible).
	invoice, err := d.lnd.AddHoldInvoice(hash, amountSats, fmt.Sprintf("tylswap-%s", swapID))
	if err != nil {
		unreserve()
		return nil, fmt.Errorf("create invoice: %w", err)
	}

	// Lock tyBTC in Tylium HTLC (irreversible until timelock).
	htlcID, l1TxID, err := d.lockHTLC(recipient, hash, amountTyBTC, timelock)
	if err != nil {
		d.lnd.CancelInvoice(hash) // best-effort cleanup
		unreserve()
		return nil, fmt.Errorf("lock HTLC: %w", err)
	}

	// Track swap — upgrade reservation to real swap.
	s := &Swap{
		ID:          swapID,
		Direction:   Buy,
		State:       StateInvoiceCreated,
		Hash:        hash,
		AmountTyBTC: amountTyBTC,
		AmountSats:  amountSats,
		HTLCID:      htlcID,
		Timelock:    timelock,
		Invoice:     invoice,
		Recipient:   recipient,
	}

	d.mu.Lock()
	d.swaps[swapID] = s
	d.activeHashes[hash] = swapID
	d.mu.Unlock()
	d.persistSwaps()

	log.Printf("buy swap %s: locked %d tyBTC in HTLC %d, invoice created", swapID, amountTyBTC, htlcID)
	return &BuyResponse{SwapID: swapID, Invoice: invoice, HTLCID: htlcID, L1TxID: l1TxID}, nil
}

// === Sell flow: user sends tyBTC, receives Lightning BTC ===

type SellRequest struct {
	HTLCID  uint64 `json:"htlcID"`  // Tylium HTLC ID (user-created)
	Invoice string `json:"invoice"` // User's Lightning invoice to pay
}

type SellResponse struct {
	SwapID string `json:"swapID"`
}

func (d *Daemon) handleSell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)

	var req SellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	resp, err := d.initSellSwap(req.HTLCID, req.Invoice)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonReply(w, resp)
}

func (d *Daemon) initSellSwap(htlcID uint64, invoice string) (*SellResponse, error) {
	// Verify HTLC exists and is addressed to the daemon.
	htlc, err := d.tyl.GetHTLC(htlcID)
	if err != nil {
		return nil, fmt.Errorf("query HTLC: %w", err)
	}
	if htlc.Status != 0 {
		return nil, fmt.Errorf("HTLC %d not pending (status=%d)", htlcID, htlc.Status)
	}
	if htlc.RecipientID != d.addr.CallerID() {
		return nil, fmt.Errorf("HTLC %d recipient is not this daemon", htlcID)
	}

	// Reject duplicates: check hashlock and HTLC ID.
	d.mu.Lock()
	if existingID, ok := d.activeHashes[htlc.Hashlock]; ok {
		d.mu.Unlock()
		return nil, fmt.Errorf("HTLC hashlock already in use by swap %s", existingID)
	}
	for _, existing := range d.swaps {
		if existing.HTLCID == htlcID && existing.State != StateSettled && existing.State != StateFailed && existing.State != StateRefunded {
			d.mu.Unlock()
			return nil, fmt.Errorf("HTLC %d already tracked by swap %s", htlcID, existing.ID)
		}
	}
	d.activeHashes[htlc.Hashlock] = "" // reserve
	d.mu.Unlock()

	unreserve := func() {
		d.mu.Lock()
		delete(d.activeHashes, htlc.Hashlock)
		d.mu.Unlock()
	}

	// Verify timelock gives enough time to claim.
	height, err := d.tyl.GetBlockHeight()
	if err != nil {
		unreserve()
		return nil, fmt.Errorf("get height: %w", err)
	}
	if htlc.Timelock < height+MinSellTimelock {
		unreserve()
		return nil, fmt.Errorf("HTLC %d timelock too short: expires at %d, current height %d (need %d+ blocks remaining)",
			htlcID, htlc.Timelock, height, MinSellTimelock)
	}

	// Decode and validate the invoice.
	decoded, err := d.lnd.DecodePayReq(invoice)
	if err != nil {
		unreserve()
		return nil, fmt.Errorf("decode invoice: %w", err)
	}

	// Verify invoice payment hash matches HTLC hashlock.
	invoiceHashBytes, err := hex.DecodeString(decoded.PaymentHash)
	if err != nil || len(invoiceHashBytes) != 32 {
		unreserve()
		return nil, fmt.Errorf("invalid invoice payment hash")
	}
	var invoiceHash [32]byte
	copy(invoiceHash[:], invoiceHashBytes)
	if invoiceHash != htlc.Hashlock {
		unreserve()
		return nil, fmt.Errorf("invoice payment hash does not match HTLC hashlock")
	}

	// Calculate sats to pay (1:1 - fee).
	amountSats := int64(htlc.Amount)
	if d.cfg.FeeBPS > 0 {
		amountSats = amountSats * int64(10000-d.cfg.FeeBPS) / 10000
	}

	if decoded.NumSatoshis > amountSats {
		unreserve()
		return nil, fmt.Errorf("invoice amount %d sats exceeds allowed %d sats", decoded.NumSatoshis, amountSats)
	}

	swapID := d.newSwapID()
	s := &Swap{
		ID:          swapID,
		Direction:   Sell,
		State:       StateHTLCLocked,
		Hash:        htlc.Hashlock,
		AmountTyBTC: htlc.Amount,
		AmountSats:  amountSats,
		HTLCID:      htlcID,
		Timelock:    htlc.Timelock,
		Invoice:     invoice,
		Verified:    true, // sell swaps read HTLC directly, ID is known-good
	}

	d.mu.Lock()
	d.swaps[swapID] = s
	d.activeHashes[htlc.Hashlock] = swapID
	d.mu.Unlock()
	d.persistSwaps()

	// Pay the user's invoice and claim in background.
	go d.executeSell(swapID)

	log.Printf("sell swap %s: HTLC %d (%d tyBTC), paying %d sats", swapID, htlcID, htlc.Amount, amountSats)
	return &SellResponse{SwapID: swapID}, nil
}

func (d *Daemon) executeSell(swapID string) {
	d.mu.Lock()
	s := d.swaps[swapID]
	d.mu.Unlock()

	// Pay the Lightning invoice.
	d.updateState(swapID, StatePaymentInFlight)
	preimage, err := d.lnd.SendPayment(s.Invoice, 60)
	if err != nil {
		d.failSwap(swapID, fmt.Sprintf("LN payment failed: %v", err))
		return
	}

	// Persist preimage to disk immediately — crash recovery depends on this.
	d.persistPreimage(s.HTLCID, preimage)

	d.mu.Lock()
	s.Preimage = preimage
	d.mu.Unlock()
	d.updateState(swapID, StateClaimed)
	log.Printf("sell swap %s: LN payment succeeded, preimage: %s", swapID, hex.EncodeToString(preimage[:]))

	// Claim the Tylium HTLC with the preimage.
	if err := d.claimHTLC(s.HTLCID, preimage); err != nil {
		// Payment went through but claim failed — we'll retry in the monitor.
		log.Printf("sell swap %s: WARNING: claim failed: %v (will retry)", swapID, err)
		return
	}

	d.updateState(swapID, StateSettled)
	log.Printf("sell swap %s: settled", swapID)
}

// === Monitor: watch for HTLC claims and settle invoices ===

func (d *Daemon) monitor(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkSwaps()
		}
	}
}

func (d *Daemon) checkSwaps() {
	d.mu.Lock()
	active := make([]*Swap, 0)
	for _, s := range d.swaps {
		if s.State != StateSettled && s.State != StateFailed && s.State != StateRefunded {
			active = append(active, s)
		}
	}
	d.mu.Unlock()

	for _, s := range active {
		switch s.Direction {
		case Buy:
			d.checkBuySwap(s)
		case Sell:
			d.checkSellSwap(s)
		}
	}
}

// verifyHTLCID confirms the on-chain HTLC at the expected ID matches this swap.
// If the ID shifted (due to a race in lockHTLC), scans to find the correct one.
func (d *Daemon) verifyHTLCID(s *Swap) bool {
	// Check the expected ID first.
	htlc, err := d.tyl.GetHTLC(s.HTLCID)
	if err != nil {
		return false
	}
	if htlc.Amount == s.AmountTyBTC && htlc.Hashlock == s.Hash {
		d.mu.Lock()
		s.Verified = true
		d.mu.Unlock()
		d.persistSwaps()
		return true
	}

	// If amount is 0 at the expected ID, the HTLC may not be mined yet.
	if htlc.Amount == 0 {
		return false
	}

	// Expected ID has a different HTLC. Scan forward to find ours.
	counter, err := d.tyl.GetNextHTLCID()
	if err != nil || counter == 0 {
		return false
	}
	for id := s.HTLCID + 1; id < counter; id++ {
		h, err := d.tyl.GetHTLC(id)
		if err != nil {
			continue
		}
		if h.Hashlock == s.Hash && h.Amount == s.AmountTyBTC {
			log.Printf("buy swap %s: HTLC ID corrected %d -> %d", s.ID, s.HTLCID, id)
			d.mu.Lock()
			s.HTLCID = id
			s.Verified = true
			d.mu.Unlock()
			d.persistSwaps()
			return true
		}
	}
	return false
}

func (d *Daemon) checkBuySwap(s *Swap) {
	// For buy swaps, we're waiting for the user to claim the Tylium HTLC.
	// Once claimed, the preimage is revealed on-chain and we settle the LN invoice.

	// Verify HTLC ID is correct before any operations (handles lockHTLC race).
	if !s.Verified && s.State != StateRefundSubmitted {
		if !d.verifyHTLCID(s) {
			return
		}
	}

	// Wait for refund to finalize before canceling the invoice.
	if s.State == StateRefundSubmitted {
		htlc, finalized, err := d.tyl.GetFinalizedHTLC(s.HTLCID)
		if err != nil || !finalized {
			return
		}
		if htlc.Status == 2 { // refunded and finalized
			d.lnd.CancelInvoice(s.Hash)
			d.updateState(s.ID, StateRefunded)
			log.Printf("buy swap %s: refund finalized, invoice canceled", s.ID)
		} else if htlc.Status == 1 { // user claimed before refund was mined
			if err := d.lnd.SettleInvoice(htlc.Preimage); err != nil {
				log.Printf("buy swap %s: settle invoice failed: %v (will retry)", s.ID, err)
				return
			}
			d.mu.Lock()
			s.Preimage = htlc.Preimage
			d.mu.Unlock()
			d.updateState(s.ID, StateSettled)
			log.Printf("buy swap %s: user claimed during refund race, settled", s.ID)
		}
		return
	}

	if s.State != StateInvoiceCreated && s.State != StatePaymentInFlight {
		return
	}

	// Check if LN invoice has been paid (ACCEPTED state).
	invoiceState, err := d.lnd.LookupInvoice(s.Hash)
	if err != nil {
		log.Printf("buy swap %s: LN lookup error: %v", s.ID, err)
	} else if invoiceState == lnd.InvoiceAccepted && s.State == StateInvoiceCreated {
		d.updateState(s.ID, StatePaymentInFlight)
		log.Printf("buy swap %s: LN payment received, waiting for HTLC claim", s.ID)
	}

	// Check if HTLC has been claimed on Tylium (finalized state only).
	htlc, finalized, err := d.tyl.GetFinalizedHTLC(s.HTLCID)
	if err != nil {
		return
	}
	if !finalized {
		// Fall back to unfinalized state for timelock checks.
		htlcUnfin, err := d.tyl.GetHTLC(s.HTLCID)
		if err != nil {
			return
		}
		if htlcUnfin.Status == 0 {
			height, _ := d.tyl.GetBlockHeight()
			if height >= s.Timelock {
				if err := d.refundHTLC(s.HTLCID); err != nil {
					log.Printf("buy swap %s: refund failed: %v", s.ID, err)
					return
				}
				// Do NOT cancel invoice yet — wait for refund to finalize.
				d.updateState(s.ID, StateRefundSubmitted)
				log.Printf("buy swap %s: refund submitted, waiting for finalization", s.ID)
			}
		}
		return
	}

	if htlc.Status == 1 { // claimed and finalized
		// Extract preimage and settle LN invoice.
		if err := d.lnd.SettleInvoice(htlc.Preimage); err != nil {
			log.Printf("buy swap %s: settle invoice failed: %v (will retry)", s.ID, err)
			return
		}
		d.mu.Lock()
		s.Preimage = htlc.Preimage
		d.mu.Unlock()
		d.updateState(s.ID, StateSettled)
		log.Printf("buy swap %s: settled (preimage: %s)", s.ID, hex.EncodeToString(htlc.Preimage[:]))
	}

	// Check for timelock expiry — submit refund if HTLC is still pending.
	if htlc.Status == 0 {
		height, _ := d.tyl.GetBlockHeight()
		if height >= s.Timelock {
			if err := d.refundHTLC(s.HTLCID); err != nil {
				log.Printf("buy swap %s: refund failed: %v", s.ID, err)
				return
			}
			d.updateState(s.ID, StateRefundSubmitted)
			log.Printf("buy swap %s: refund submitted, waiting for finalization", s.ID)
		}
	}
}

func (d *Daemon) checkSellSwap(s *Swap) {
	// For sell swaps that failed to claim, retry.
	if s.State == StateClaimed {
		var preimage [32]byte
		d.mu.Lock()
		preimage = s.Preimage
		d.mu.Unlock()

		if preimage != [32]byte{} {
			if err := d.claimHTLC(s.HTLCID, preimage); err != nil {
				log.Printf("sell swap %s: claim retry failed: %v", s.ID, err)
				return
			}
			d.updateState(s.ID, StateSettled)
			log.Printf("sell swap %s: claim retry succeeded", s.ID)
		}
	}
}

// === HTLC operations ===

func (d *Daemon) lockHTLC(recipient types.Address, hash [32]byte, amount, timelock uint64) (uint64, string, error) {
	recipientID := recipient.CallerID()
	hw0 := binary.BigEndian.Uint64(hash[0:8])
	hw1 := binary.BigEndian.Uint64(hash[8:16])
	hw2 := binary.BigEndian.Uint64(hash[16:24])
	hw3 := binary.BigEndian.Uint64(hash[24:32])

	data := packCallData(executor.HTLCFnLock, recipientID, timelock, hw0, hw1, hw2, hw3)

	nonce, _, err := d.tyl.GetAccount(d.addr)
	if err != nil {
		return 0, "", err
	}

	// Get next HTLC ID before submitting.
	nextID, err := d.tyl.GetNextHTLCID()
	if err != nil {
		return 0, "", err
	}

	tx := &types.Transaction{
		Version:  1,
		Nonce:    nonce,
		From:     d.addr,
		To:       executor.HTLCSystemAddress,
		Value:    amount,
		GasPrice: 1,
		GasLimit: 100_000,
		Data:     data,
	}

	sig, err := crypto.Sign(tx.SigningHash(), d.privKey)
	if err != nil {
		return 0, "", err
	}
	tx.Signature = sig

	txBytes := encoding.EncodeTx(tx)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, txBytes)

	txid, err := bitcoin.EmbedEnvelope(d.btc, envelope)
	if err != nil {
		return 0, "", fmt.Errorf("embed HTLC lock: %w", err)
	}
	log.Printf("HTLC lock submitted: txid=%s, expected htlcID=%d", txid, nextID)

	return nextID, txid, nil
}

func (d *Daemon) claimHTLC(htlcID uint64, preimage [32]byte) error {
	pw0 := binary.BigEndian.Uint64(preimage[0:8])
	pw1 := binary.BigEndian.Uint64(preimage[8:16])
	pw2 := binary.BigEndian.Uint64(preimage[16:24])
	pw3 := binary.BigEndian.Uint64(preimage[24:32])

	data := packCallData(executor.HTLCFnClaim, htlcID, pw0, pw1, pw2, pw3)

	nonce, _, err := d.tyl.GetAccount(d.addr)
	if err != nil {
		return err
	}

	tx := &types.Transaction{
		Version: 1,
		Nonce:   nonce,
		From:    d.addr,
		To:      executor.HTLCSystemAddress,
		Data:    data,
	}

	sig, err := crypto.Sign(tx.SigningHash(), d.privKey)
	if err != nil {
		return err
	}
	tx.Signature = sig

	txBytes := encoding.EncodeTx(tx)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, txBytes)

	txid, err := bitcoin.EmbedEnvelope(d.btc, envelope)
	if err != nil {
		return fmt.Errorf("embed HTLC claim: %w", err)
	}
	log.Printf("HTLC claim submitted: txid=%s, htlcID=%d", txid, htlcID)
	return nil
}

func (d *Daemon) refundHTLC(htlcID uint64) error {
	data := packCallData(executor.HTLCFnRefund, htlcID)

	nonce, _, err := d.tyl.GetAccount(d.addr)
	if err != nil {
		return err
	}

	tx := &types.Transaction{
		Version:  1,
		Nonce:    nonce,
		From:     d.addr,
		To:       executor.HTLCSystemAddress,
		GasPrice: 1,
		GasLimit: 100_000,
		Data:     data,
	}

	sig, err := crypto.Sign(tx.SigningHash(), d.privKey)
	if err != nil {
		return err
	}
	tx.Signature = sig

	txBytes := encoding.EncodeTx(tx)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, txBytes)

	txid, err := bitcoin.EmbedEnvelope(d.btc, envelope)
	if err != nil {
		return fmt.Errorf("embed HTLC refund: %w", err)
	}
	log.Printf("HTLC refund submitted: txid=%s, htlcID=%d", txid, htlcID)
	return nil
}

// === Persistence ===

func (d *Daemon) swapsFile() string {
	if d.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(d.cfg.DataDir, "swaps.json")
}

func (d *Daemon) preimageFile(htlcID uint64) string {
	if d.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(d.cfg.DataDir, "preimages", fmt.Sprintf("%d", htlcID))
}

// persistSwaps writes all swaps to disk.
func (d *Daemon) persistSwaps() {
	path := d.swapsFile()
	if path == "" {
		return
	}
	d.mu.Lock()
	data, err := json.MarshalIndent(d.swaps, "", "  ")
	d.mu.Unlock()
	if err != nil {
		log.Printf("warning: failed to marshal swaps: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("warning: failed to persist swaps: %v", err)
	}
}

// loadSwaps restores swap state from disk.
func (d *Daemon) loadSwaps() {
	path := d.swapsFile()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // no file yet
	}
	var swaps map[string]*Swap
	if err := json.Unmarshal(data, &swaps); err != nil {
		log.Printf("warning: failed to load swaps: %v", err)
		return
	}

	d.mu.Lock()
	d.swaps = swaps
	// Rebuild activeHashes and find max seq.
	for _, s := range swaps {
		if s.State != StateSettled && s.State != StateFailed && s.State != StateRefunded {
			d.activeHashes[s.Hash] = s.ID
		}
		// Restore preimages from disk for sell swaps that need retry.
		if s.Direction == Sell && s.State == StateClaimed && s.Preimage == [32]byte{} {
			if pre, err := os.ReadFile(d.preimageFile(s.HTLCID)); err == nil && len(pre) == 32 {
				copy(s.Preimage[:], pre)
				log.Printf("restored preimage for sell swap %s (HTLC %d)", s.ID, s.HTLCID)
			}
		}
	}
	// Restore sequence counter.
	for id := range swaps {
		var n int
		if _, err := fmt.Sscanf(id, "swap-%d", &n); err == nil && n > d.seq {
			d.seq = n
		}
	}
	d.mu.Unlock()

	log.Printf("restored %d swaps from disk", len(swaps))
}

// persistPreimage saves a preimage to disk for crash recovery.
func (d *Daemon) persistPreimage(htlcID uint64, preimage [32]byte) {
	path := d.preimageFile(htlcID)
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, preimage[:], 0o600); err != nil {
		log.Printf("WARNING: failed to persist preimage for HTLC %d: %v", htlcID, err)
	}
}

// === HTTP API helpers ===

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	d.mu.Lock()
	s, ok := d.swaps[id]
	d.mu.Unlock()
	if !ok {
		jsonError(w, "swap not found", http.StatusNotFound)
		return
	}
	jsonReply(w, s)
}

func (d *Daemon) handleList(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	list := make([]*Swap, 0, len(d.swaps))
	for _, s := range d.swaps {
		list = append(list, s)
	}
	d.mu.Unlock()
	jsonReply(w, list)
}

func (d *Daemon) updateState(swapID string, state State) {
	d.mu.Lock()
	if s, ok := d.swaps[swapID]; ok {
		s.State = state
		// Remove from activeHashes when terminal.
		if state == StateSettled || state == StateFailed || state == StateRefunded {
			delete(d.activeHashes, s.Hash)
		}
	}
	d.mu.Unlock()
	d.persistSwaps()
}

func (d *Daemon) failSwap(swapID string, errMsg string) {
	d.mu.Lock()
	if s, ok := d.swaps[swapID]; ok {
		s.State = StateFailed
		s.Error = errMsg
		delete(d.activeHashes, s.Hash)
	}
	d.mu.Unlock()
	d.persistSwaps()
	log.Printf("swap %s failed: %s", swapID, errMsg)
}

func (d *Daemon) newSwapID() string {
	d.mu.Lock()
	d.seq++
	id := fmt.Sprintf("swap-%d", d.seq)
	d.mu.Unlock()
	return id
}

func packCallData(args ...uint64) []byte {
	data := make([]byte, len(args)*8)
	for i, arg := range args {
		binary.BigEndian.PutUint64(data[i*8:], arg)
	}
	return data
}

func jsonReply(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
