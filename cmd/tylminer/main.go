package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/djkazic/tylium/internal/miner"
	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/types"
)

func main() {
	var (
		btcRPC      string
		btcUser     string
		btcPass     string
		btcWallet   string
		tylRPC      string
		privKeyHex  string
		dataDir     string
		mineSeconds int
		regtest     bool
	)

	flag.StringVar(&btcRPC, "btcrpc", "http://127.0.0.1:18332", "Bitcoin Core RPC endpoint")
	flag.StringVar(&btcUser, "btcuser", "tylium", "Bitcoin RPC username")
	flag.StringVar(&btcPass, "btcpass", "tylium", "Bitcoin RPC password")
	flag.StringVar(&btcWallet, "wallet", "default", "Bitcoin Core wallet name")
	flag.StringVar(&tylRPC, "tylrpc", "http://127.0.0.1:19332", "Tylium node RPC endpoint")
	flag.StringVar(&privKeyHex, "key", "", "Miner private key (hex)")
	flag.StringVar(&dataDir, "datadir", "", "Directory to persist miner state (optional)")
	flag.IntVar(&mineSeconds, "duration", 10, "Mining duration per round in seconds")
	flag.BoolVar(&regtest, "regtest", false, "Auto-mine a block after submitting checkpoint (regtest only)")
	flag.Parse()

	if privKeyHex == "" {
		fmt.Fprintln(os.Stderr, "error: --key is required")
		os.Exit(1)
	}

	keyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		log.Fatalf("invalid private key hex: %v", err)
	}
	privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}

	minerAddr := privKey.Public().Address()
	log.Printf("tylminer starting, address=%s, duration=%ds", minerAddr.Hex(), mineSeconds)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	btc := bitcoin.NewRPCClient(btcRPC, btcUser, btcPass)
	btc.EnsureWallet(btcWallet)
	m := miner.New(privKey)

	var prevChecksum types.Hash256
	var lastRoot types.Hash256

	// Restore last attested root from disk to avoid duplicate submissions.
	lastRootFile := ""
	if dataDir != "" {
		os.MkdirAll(dataDir, 0o755)
		lastRootFile = filepath.Join(dataDir, "last_attested_root")
		if b, err := os.ReadFile(lastRootFile); err == nil && len(b) == 32 {
			copy(lastRoot[:], b)
			log.Printf("restored last attested root: %s", lastRoot.Hex())
		}
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("tylminer stopped")
			return
		default:
		}

		// Poll tyld for current state root.
		root, err := getStateRoot(tylRPC)
		if err != nil {
			log.Printf("waiting for tyld: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if root.IsZero() {
			time.Sleep(2 * time.Second)
			continue
		}

		if root == lastRoot {
			// No new state — wait for next block.
			time.Sleep(1 * time.Second)
			continue
		}

		lastRoot = root
		log.Printf("new state root: %s — mining...", root.Hex())

		mineCtx, mineCancel := context.WithTimeout(ctx, time.Duration(mineSeconds)*time.Second)
		checkpoint, err := m.Mine(mineCtx, root, prevChecksum)
		mineCancel()

		if err != nil {
			log.Printf("mining round failed: %v", err)
			continue
		}

		dist := miner.Distance(checkpoint.AttestationHash, root)
		log.Printf("attestation: distance=%s", dist.Text(16))

		// Submit checkpoint to L1.
		cpBytes := encoding.EncodeCheckpoint(checkpoint)
		envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeCheckpoint, cpBytes)

		txid, err := bitcoin.EmbedEnvelope(btc, envelope)
		if err != nil {
			log.Printf("L1 submit failed: %v", err)
			continue
		}

		// On regtest, auto-mine a block so the checkpoint is confirmed immediately.
		if regtest {
			if err := btc.MineBlock(); err != nil {
				log.Printf("warning: could not mine block: %v", err)
			}
		}

		log.Printf("checkpoint submitted: txid=%s", txid)
		prevChecksum = checkpoint.AttestationHash

		// Persist last attested root so restarts don't duplicate.
		if lastRootFile != "" {
			os.WriteFile(lastRootFile, root[:], 0o644)
		}
	}
}

// getStateRoot calls tyl_stateRoot on the tylium node.
func getStateRoot(endpoint string) (types.Hash256, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tyl_stateRoot",
		"params":  []interface{}{},
	})

	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return types.ZeroHash, err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result struct {
			StateRoot string `json:"stateRoot"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return types.ZeroHash, err
	}
	if rpcResp.Error != nil {
		return types.ZeroHash, fmt.Errorf("%s", rpcResp.Error.Message)
	}

	rootBytes, err := hex.DecodeString(rpcResp.Result.StateRoot)
	if err != nil {
		return types.ZeroHash, err
	}
	return types.BytesToHash256(rootBytes), nil
}

