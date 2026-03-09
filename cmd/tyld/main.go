package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/djkazic/tylium/internal/node"
)

func main() {
	cfg := &node.Config{}

	flag.StringVar(&cfg.BitcoinRPC, "btcrpc", "http://127.0.0.1:18332", "Bitcoin Core RPC endpoint")
	flag.StringVar(&cfg.BitcoinUser, "btcuser", "tylium", "Bitcoin RPC username")
	flag.StringVar(&cfg.BitcoinPass, "btcpass", "tylium", "Bitcoin RPC password")
	flag.Uint64Var(&cfg.GenesisHeight, "genesis", 0, "L1 block height where Tylium begins")
	flag.StringVar(&cfg.DataDir, "datadir", ".tylium", "Data directory for persistent state")
	flag.StringVar(&cfg.RPCAddr, "rpcaddr", ":19332", "JSON-RPC listen address")
	flag.Parse()

	if cfg.GenesisHeight == 0 {
		fmt.Fprintln(os.Stderr, "error: --genesis flag is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	n, err := node.New(cfg)
	if err != nil {
		log.Fatalf("failed to create node: %v", err)
	}
	defer n.Close()

	log.Printf("tylium node starting, genesis=%d, datadir=%s, rpc=%s", cfg.GenesisHeight, cfg.DataDir, cfg.RPCAddr)

	// Sync historical blocks first.
	if err := n.Sync(ctx); err != nil {
		log.Fatalf("sync failed: %v", err)
	}

	// Follow new blocks.
	if err := n.Follow(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("follow failed: %v", err)
	}

	log.Println("tylium node stopped")
}
