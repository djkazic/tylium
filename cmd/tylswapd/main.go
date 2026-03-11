package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/djkazic/tylium/internal/lnd"
	"github.com/djkazic/tylium/internal/swap"
	"github.com/djkazic/tylium/pkg/crypto"
)

func main() {
	var (
		btcRPC      string
		btcUser     string
		btcPass     string
		btcWallet   string
		tylRPC      string
		lndRest     string
		lndCert     string
		lndMac      string
		privKeyHex  string
		listenAddr  string
		dataDir     string
		apiKey      string
		feeBPS      int
		timelock    uint64
		maxSwapSize uint64
	)

	home, _ := os.UserHomeDir()
	defaultCert := filepath.Join(home, ".lnd", "tls.cert")
	defaultMac := filepath.Join(home, ".lnd", "data", "chain", "bitcoin", "testnet", "admin.macaroon")

	flag.StringVar(&btcRPC, "btcrpc", "http://127.0.0.1:18332", "Bitcoin Core RPC endpoint")
	flag.StringVar(&btcUser, "btcuser", "tylium", "Bitcoin RPC username")
	flag.StringVar(&btcPass, "btcpass", "tylium", "Bitcoin RPC password")
	flag.StringVar(&btcWallet, "wallet", "default", "Bitcoin Core wallet name")
	flag.StringVar(&tylRPC, "tylrpc", "http://127.0.0.1:19332", "Tylium node RPC endpoint")
	flag.StringVar(&lndRest, "lndrest", "https://127.0.0.1:8080", "LND REST endpoint")
	flag.StringVar(&lndCert, "lndcert", defaultCert, "LND TLS certificate path")
	flag.StringVar(&lndMac, "lndmacaroon", defaultMac, "LND macaroon path")
	flag.StringVar(&privKeyHex, "key", "", "Private key (hex) for signing Tylium transactions")
	flag.StringVar(&listenAddr, "listen", "127.0.0.1:19333", "HTTP API listen address")
	flag.StringVar(&dataDir, "datadir", "", "Directory for persistent swap state (required)")
	flag.StringVar(&apiKey, "apikey", "", "API key for authentication (optional)")
	flag.IntVar(&feeBPS, "fee", 50, "Swap fee in basis points (50 = 0.5%)")
	flag.Uint64Var(&timelock, "timelock", 144, "HTLC timelock in blocks")
	flag.Uint64Var(&maxSwapSize, "max-swap", 0, "Maximum swap size in tyBTC (0 = unlimited)")
	flag.Parse()

	if privKeyHex == "" {
		fmt.Fprintln(os.Stderr, "error: --key is required")
		os.Exit(1)
	}

	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "error: --datadir is required")
		os.Exit(1)
	}

	if feeBPS < 0 || feeBPS > 10000 {
		fmt.Fprintf(os.Stderr, "error: --fee must be 0-10000 (got %d)\n", feeBPS)
		os.Exit(1)
	}

	os.MkdirAll(dataDir, 0o755)

	keyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		log.Fatalf("invalid private key hex: %v", err)
	}
	privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}

	lndClient, err := lnd.NewClient(lndRest, lndCert, lndMac)
	if err != nil {
		log.Fatalf("lnd client: %v", err)
	}

	// Verify LND connectivity.
	info, err := lndClient.GetInfo()
	if err != nil {
		log.Fatalf("lnd connection failed: %v", err)
	}

	addr := privKey.Public().Address()
	log.Printf("tylswapd starting")
	log.Printf("  address:  %s", addr.Hex())
	log.Printf("  lnd node: %s (%s)", info.PubKey, info.Alias)
	log.Printf("  fee:      %d bps (%.2f%%)", feeBPS, float64(feeBPS)/100)
	log.Printf("  timelock: %d blocks", timelock)
	log.Printf("  datadir:  %s", dataDir)
	if maxSwapSize > 0 {
		log.Printf("  max swap: %d tyBTC", maxSwapSize)
	}
	if apiKey != "" {
		log.Printf("  auth:     enabled")
	} else {
		log.Printf("  WARNING:  no --apikey set, API is unauthenticated")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	d := swap.New(swap.Config{
		TylRPC:     tylRPC,
		BtcRPC:     btcRPC,
		BtcUser:    btcUser,
		BtcPass:    btcPass,
		BtcWallet:  btcWallet,
		LNDRest:    lndRest,
		LNDCert:    lndCert,
		LNDMac:     lndMac,
		PrivKey:    privKey,
		ListenAddr: listenAddr,
		DataDir:    dataDir,
		APIKey:     apiKey,
		FeeBPS:      feeBPS,
		Timelock:    timelock,
		MaxSwapSize: maxSwapSize,
	}, lndClient)

	if err := d.Run(ctx); err != nil {
		log.Fatalf("tylswapd: %v", err)
	}
}
