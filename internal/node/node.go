package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/internal/miner"
	"github.com/djkazic/tylium/internal/rpc"
	"github.com/djkazic/tylium/internal/scanner"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/internal/store"
	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

// Node is the Tylium full node that scans L1, executes transactions,
// and maintains the global state.
type Node struct {
	config   *Config
	btc      bitcoin.Client
	scanner  *scanner.Scanner
	executor *executor.Executor
	stateDB  *state.StateDB
	store    *store.Store
	rpcSrv   *rpc.Server

	// Current chain tip (optimistic — not yet attested).
	lastHeight uint64
	stateRoot  types.Hash256

	// Finalized state — confirmed by miner attestation.
	finalizedHeight uint64
	finalizedRoot   types.Hash256

	// Last accepted checkpoint (for miner coinbase selection).
	lastCheckpoint *types.Checkpoint

	// Maps state root → block height for finalization lookups.
	rootToHeight map[types.Hash256]uint64
}

// New creates a Tylium node with the given configuration.
// If DataDir is set, persistent LevelDB storage is used; otherwise in-memory.
func New(cfg *Config) (*Node, error) {
	btc := bitcoin.NewRPCClient(cfg.BitcoinRPC, cfg.BitcoinUser, cfg.BitcoinPass)
	scan := scanner.New(btc)

	n := &Node{
		config:       cfg,
		btc:          btc,
		scanner:      scan,
		lastHeight:   cfg.GenesisHeight,
		rootToHeight: make(map[types.Hash256]uint64),
	}

	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
			return nil, fmt.Errorf("create datadir: %w", err)
		}
		st, err := store.Open(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("open store: %w", err)
		}
		n.store = st

		stateDB := state.NewStateDBWithFactory(
			st.StateStore(),
			func(addr types.Address) merkle.NodeStore {
				return st.StorageStore(addr)
			},
		)
		n.stateDB = stateDB

		// Restore last known height (never go below genesis).
		if h, err := st.GetHeight(); err == nil && h > cfg.GenesisHeight {
			n.lastHeight = h
		}
		if root, err := st.GetStateRoot(); err == nil && !root.IsZero() {
			n.stateRoot = root
			stateDB.LoadTrieRoot(root)
		}
		if h, err := st.GetFinalizedHeight(); err == nil && h > 0 {
			n.finalizedHeight = h
		}
		if root, err := st.GetFinalizedRoot(); err == nil && !root.IsZero() {
			n.finalizedRoot = root
		}

		// Restore contract bytecodes from persistent storage into the in-memory StateDB.
		if codes, err := st.ListCodes(); err == nil {
			for hash, code := range codes {
				stateDB.LoadCode(hash, code)
			}
		} else {
			log.Printf("warning: failed to load contract codes: %v", err)
		}
	} else {
		memStore := merkle.NewMemStore()
		n.stateDB = state.NewStateDB(memStore)
	}

	n.executor = executor.New(n.stateDB)

	// Restore contract registry from persistent storage.
	if n.store != nil {
		if contracts, err := n.store.ListContracts(); err == nil {
			for callerID, addr := range contracts {
				n.executor.RegisterContract(callerID, addr)
			}
			if len(contracts) > 0 {
				log.Printf("restored %d contracts from store", len(contracts))
			}
		}
	}

	// Start RPC server if configured.
	if cfg.RPCAddr != "" {
		handler := rpc.NewHandler(n, n.stateDB, n.store)
		n.rpcSrv = rpc.NewServer(handler)
		if err := n.rpcSrv.Start(cfg.RPCAddr); err != nil {
			return nil, fmt.Errorf("start rpc: %w", err)
		}
	}

	return n, nil
}

// Close shuts down the node and all resources.
func (n *Node) Close() error {
	if n.rpcSrv != nil {
		n.rpcSrv.Stop()
	}
	if n.store != nil {
		return n.store.Close()
	}
	return nil
}

// Sync replays all L1 blocks from genesis (or last known height) to tip.
// This enables full state reconstruction from nothing but L1 history.
func (n *Node) Sync(ctx context.Context) error {
	tip, err := n.btc.GetBestBlockHeight()
	if err != nil {
		return fmt.Errorf("get best block: %w", err)
	}

	if n.lastHeight >= tip {
		log.Printf("already synced to block %d", n.lastHeight)
		return nil
	}

	log.Printf("syncing from block %d to %d", n.lastHeight+1, tip)

	for height := n.lastHeight + 1; height <= tip; height++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := n.processBlock(height); err != nil {
			return fmt.Errorf("process block %d: %w", height, err)
		}

		if height%100 == 0 {
			log.Printf("synced to block %d, state root: %s", height, n.stateRoot.Hex())
		}
	}

	log.Printf("sync complete at block %d, state root: %s", n.lastHeight, n.stateRoot.Hex())
	return nil
}

// Follow subscribes to new L1 blocks and processes them as they arrive.
func (n *Node) Follow(ctx context.Context) error {
	results, err := n.scanner.Subscribe(ctx, n.lastHeight)
	if err != nil {
		return err
	}

	for result := range results {
		// Check for L1 reorg before processing.
		if err := n.detectAndHandleReorg(result); err != nil {
			log.Printf("reorg handling error at height %d: %v", result.L1Height, err)
			continue
		}

		// Process any checkpoints found in this L1 block.
		// The winning miner becomes the coinbase for this block's gas fees.
		n.processCheckpoints(result.Checkpoints)

		block, receipts, err := n.executor.ProcessBlock(
			result.L1Height,
			result.L1BlockHash,
			result.Payloads,
		)
		if err != nil {
			log.Printf("error processing block %d: %v", result.L1Height, err)
			continue
		}

		n.lastHeight = block.Height
		n.stateRoot = block.StateRoot
		n.rootToHeight[block.StateRoot] = block.Height
		n.persist(block, receipts)
		if len(result.Checkpoints) > 0 {
			n.tryFinalize()
		}

		log.Printf("block %d: %d txs, %d gas, coinbase: %s, state root: %s (finalized: %d)",
			block.Height, len(block.Transactions), block.GasUsed, block.Coinbase.Hex(), block.StateRoot.Hex(), n.finalizedHeight)
	}

	return nil
}

func (n *Node) processBlock(height uint64) error {
	result, err := n.scanner.ScanBlock(height)
	if err != nil {
		return err
	}

	// Reorg detection: verify the new block's PrevHash matches what we stored.
	if err := n.detectAndHandleReorg(result); err != nil {
		return fmt.Errorf("reorg handling at height %d: %w", height, err)
	}

	// Process checkpoints before executing transactions so the winning
	// miner's coinbase is set for this block's gas fees.
	n.processCheckpoints(result.Checkpoints)

	block, receipts, err := n.executor.ProcessBlock(
		result.L1Height,
		result.L1BlockHash,
		result.Payloads,
	)
	if err != nil {
		return err
	}

	n.lastHeight = block.Height
	n.stateRoot = block.StateRoot
	n.rootToHeight[block.StateRoot] = block.Height
	n.persist(block, receipts)
	if len(result.Checkpoints) > 0 {
		n.tryFinalize()
	}
	return nil
}

// detectAndHandleReorg checks if the L1 block's parent matches what we expect.
// If not, it walks back to find the fork point and rolls back state.
func (n *Node) detectAndHandleReorg(result *scanner.ScanResult) error {
	if n.store == nil {
		return nil // in-memory mode, no reorg detection
	}

	prevHeight := result.L1Height - 1
	if prevHeight < n.config.GenesisHeight {
		return nil // at or before genesis, nothing to check
	}

	storedHash, err := n.store.GetL1Hash(prevHeight)
	if err != nil {
		return fmt.Errorf("get L1 hash for height %d: %w", prevHeight, err)
	}
	if storedHash.IsZero() {
		return nil // no stored hash (first sync), nothing to verify
	}

	if storedHash == result.L1PrevHash {
		return nil // parent matches, no reorg
	}

	// Reorg detected! Walk back to find the fork point.
	log.Printf("REORG DETECTED at height %d: expected prev %s, got %s",
		result.L1Height, storedHash.Hex(), result.L1PrevHash.Hex())

	forkHeight, err := n.findForkPoint(prevHeight)
	if err != nil {
		return fmt.Errorf("find fork point: %w", err)
	}

	log.Printf("reorg fork point: height %d, rolling back from %d", forkHeight, n.lastHeight)

	if err := n.rollbackToHeight(forkHeight); err != nil {
		return fmt.Errorf("rollback to %d: %w", forkHeight, err)
	}

	// Re-process blocks from fork point + 1 up to (but not including) the current height.
	// The current height's block will be processed by the caller.
	for h := forkHeight + 1; h < result.L1Height; h++ {
		if err := n.reprocessBlock(h); err != nil {
			return fmt.Errorf("reprocess block %d: %w", h, err)
		}
	}

	return nil
}

// findForkPoint walks backwards from the given height to find where our stored
// L1 hashes still match the actual chain.
func (n *Node) findForkPoint(fromHeight uint64) (uint64, error) {
	for h := fromHeight; h >= n.config.GenesisHeight; h-- {
		storedHash, err := n.store.GetL1Hash(h)
		if err != nil {
			return 0, err
		}
		if storedHash.IsZero() {
			return h, nil // no stored hash, this is our fork point
		}

		// Fetch the actual block hash from L1.
		block, err := n.btc.GetBlockByHeight(h)
		if err != nil {
			return 0, fmt.Errorf("get block %d: %w", h, err)
		}

		if storedHash == block.Hash {
			return h, nil // hashes match, this is the fork point
		}

		log.Printf("reorg: height %d hash mismatch (stored=%s, chain=%s)",
			h, storedHash.Hex(), block.Hash.Hex())
	}

	return n.config.GenesisHeight, nil
}

// rollbackToHeight resets state to what it was at the given height.
func (n *Node) rollbackToHeight(height uint64) error {
	if height == n.config.GenesisHeight {
		// Full reset to genesis.
		n.lastHeight = n.config.GenesisHeight
		n.stateRoot = types.ZeroHash
		n.stateDB.LoadTrieRoot(types.ZeroHash)
		n.rootToHeight = make(map[types.Hash256]uint64)
		n.executor.SetCoinbase(types.ZeroAddress)
		n.lastCheckpoint = nil

		if n.finalizedHeight > height {
			n.finalizedHeight = 0
			n.finalizedRoot = types.ZeroHash
			n.persistFinalized()
		}
	} else {
		// Load the block at fork height to get its state root.
		blockData, err := n.store.GetBlock(height)
		if err != nil || blockData == nil {
			return fmt.Errorf("no block data at height %d for rollback", height)
		}

		var block types.Block
		if err := json.Unmarshal(blockData, &block); err != nil {
			return fmt.Errorf("unmarshal block %d: %w", height, err)
		}

		n.lastHeight = height
		n.stateRoot = block.StateRoot
		n.stateDB.LoadTrieRoot(block.StateRoot)

		// Clear storage trie cache — they'll be reloaded lazily.
		n.stateDB.ClearStorageTries()

		// Rebuild rootToHeight from persisted blocks up to fork height.
		n.rootToHeight = make(map[types.Hash256]uint64)
		for h := n.config.GenesisHeight + 1; h <= height; h++ {
			bd, err := n.store.GetBlock(h)
			if err != nil || bd == nil {
				continue
			}
			var b types.Block
			if err := json.Unmarshal(bd, &b); err != nil {
				continue
			}
			n.rootToHeight[b.StateRoot] = h
		}

		n.lastCheckpoint = nil
		n.executor.SetCoinbase(types.ZeroAddress)

		// If finalized height is past the fork point, roll it back too.
		if n.finalizedHeight > height {
			n.finalizedHeight = height
			n.finalizedRoot = block.StateRoot
			n.persistFinalized()
			log.Printf("warning: reorg past finalized height, rolled back finalized to %d", height)
		}
	}

	// Clean up L1 hashes above fork point.
	if err := n.store.DeleteL1HashesAbove(height); err != nil {
		log.Printf("warning: failed to clean L1 hashes: %v", err)
	}

	n.store.PutHeight(n.lastHeight)
	n.store.PutStateRoot(n.stateRoot)

	log.Printf("rolled back to height %d, state root: %s", n.lastHeight, n.stateRoot.Hex())
	return nil
}

// reprocessBlock re-scans and re-executes a single block (used after reorg rollback).
func (n *Node) reprocessBlock(height uint64) error {
	result, err := n.scanner.ScanBlock(height)
	if err != nil {
		return err
	}

	n.processCheckpoints(result.Checkpoints)

	block, receipts, err := n.executor.ProcessBlock(
		result.L1Height,
		result.L1BlockHash,
		result.Payloads,
	)
	if err != nil {
		return err
	}

	n.lastHeight = block.Height
	n.stateRoot = block.StateRoot
	n.rootToHeight[block.StateRoot] = block.Height
	n.persist(block, receipts)
	if len(result.Checkpoints) > 0 {
		n.tryFinalize()
	}

	log.Printf("reprocessed block %d after reorg, state root: %s", height, n.stateRoot.Hex())
	return nil
}

// processCheckpoints validates checkpoint attestations and sets the best
// miner as the coinbase for gas collection.
// processCheckpoints validates checkpoints and sets the winning miner as coinbase.
// Call this BEFORE block execution so the miner receives gas fees.
func (n *Node) processCheckpoints(checkpoints []*types.Checkpoint) {
	if len(checkpoints) == 0 {
		return
	}

	var best *types.Checkpoint
	for _, cp := range checkpoints {
		if !n.validateCheckpoint(cp) {
			continue
		}

		if best == nil || miner.CompareAttestations(cp, best, cp.StateRoot) < 0 {
			best = cp
		}
	}

	if best == nil {
		return
	}

	// Derive miner address from checkpoint's public key.
	pub, err := crypto.DecompressPubKey(best.MinerPubKey[:])
	if err != nil {
		log.Printf("invalid miner pubkey in checkpoint: %v", err)
		return
	}
	minerAddr := pub.Address()

	n.executor.SetCoinbase(minerAddr)
	n.lastCheckpoint = best

	dist := miner.Distance(best.AttestationHash, best.StateRoot)
	log.Printf("checkpoint accepted: miner=%s distance=%s", minerAddr.Hex(), dist.Text(16))
}

// tryFinalize checks if the last accepted checkpoint's state root maps to a
// known block height and, if so, advances the finalized height. Call this
// AFTER block execution and rootToHeight update so the mapping is current.
func (n *Node) tryFinalize() {
	if n.lastCheckpoint == nil {
		return
	}
	h, ok := n.rootToHeight[n.lastCheckpoint.StateRoot]
	if !ok || h < n.finalizedHeight {
		return
	}
	if h > n.finalizedHeight {
		n.finalizedHeight = h
		n.finalizedRoot = n.lastCheckpoint.StateRoot
		n.persistFinalized()
		log.Printf("finalized height %d, state root: %s", h, n.lastCheckpoint.StateRoot.Hex())
	}
	// Always take/refresh the snapshot — needed on re-sync where
	// finalizedHeight was restored from disk but the in-memory
	// snapshot doesn't exist yet.
	n.stateDB.SnapshotFinalized()
}

// validateCheckpoint verifies a checkpoint's attestation hash and signature.
func (n *Node) validateCheckpoint(cp *types.Checkpoint) bool {
	// Reject stale attestations — the state root is already finalized.
	if !n.finalizedRoot.IsZero() && cp.StateRoot == n.finalizedRoot {
		log.Printf("checkpoint rejected: state root %s already finalized", cp.StateRoot.Hex())
		return false
	}

	// Verify attestation hash: H(nonce || stateRoot) == attestationHash.
	var preimage []byte
	preimage = append(preimage, cp.MinerNonce[:]...)
	preimage = append(preimage, cp.StateRoot[:]...)
	expectedHash := types.Sha256(preimage)
	if cp.AttestationHash != expectedHash {
		log.Printf("checkpoint rejected: invalid attestation hash")
		return false
	}

	// Verify signature.
	pub, err := crypto.DecompressPubKey(cp.MinerPubKey[:])
	if err != nil {
		log.Printf("checkpoint rejected: invalid pubkey: %v", err)
		return false
	}

	sigHash := checkpointSigningHash(cp)
	if !crypto.Verify(sigHash, cp.Signature, pub) {
		log.Printf("checkpoint rejected: invalid signature")
		return false
	}

	return true
}

// checkpointSigningHash must match the miner's signing hash computation.
func checkpointSigningHash(cp *types.Checkpoint) types.Hash256 {
	var buf []byte
	buf = append(buf, cp.StateRoot[:]...)
	buf = append(buf, cp.PrevChecksum[:]...)
	buf = append(buf, cp.MinerNonce[:]...)
	buf = append(buf, cp.AttestationHash[:]...)
	buf = append(buf, cp.MinerPubKey[:]...)
	return types.DoubleSha256(buf)
}

func (n *Node) persist(block *types.Block, receipts []*executor.Receipt) {
	if n.store == nil {
		return
	}

	// Flush trie nodes to LevelDB before persisting metadata.
	if err := n.stateDB.FlushTries(); err != nil {
		log.Printf("warning: failed to flush tries: %v", err)
	}

	n.store.PutHeight(block.Height)
	n.store.PutStateRoot(block.StateRoot)
	n.store.PutL1Hash(block.Height, block.L1BlockHash)

	// Store block as JSON for RPC retrieval.
	blockJSON, _ := json.Marshal(block)
	n.store.PutBlock(block.Height, blockJSON)

	// Store receipts as JSON keyed by block height.
	if len(receipts) > 0 {
		receiptsJSON, _ := json.Marshal(receipts)
		n.store.PutReceipts(block.Height, receiptsJSON)

		// Index each receipt by txid for individual lookups and history.
		// Only append to the sequential index for new txids (idempotent on re-sync).
		txCount, _ := n.store.GetTxCount()
		for _, r := range receipts {
			existing, _ := n.store.GetTxReceipt(r.TxID)
			rJSON, _ := json.Marshal(r)
			n.store.PutTxReceipt(r.TxID, rJSON)
			if existing == nil {
				n.store.PutTxIndex(txCount, r.TxID)
				txCount++
			}
		}
		n.store.PutTxCount(txCount)
	}

	// Persist contract bytecodes so RPC queries for code work after restart.
	for hash, code := range n.stateDB.AllCodes() {
		n.store.PutCode(hash, code)
	}

	// Persist contract registry for AMM settlement after restart.
	for callerID, addr := range n.executor.ContractRegistry() {
		n.store.PutContract(callerID, addr)
	}
}

// StateRoot returns the current (optimistic) state root.
func (n *Node) StateRoot() types.Hash256 {
	return n.stateRoot
}

// Height returns the last processed L1 block height (optimistic).
func (n *Node) Height() uint64 {
	return n.lastHeight
}

// FinalizedHeight returns the last attested block height.
func (n *Node) FinalizedHeight() uint64 {
	return n.finalizedHeight
}

// FinalizedRoot returns the state root of the last finalized block.
func (n *Node) FinalizedRoot() types.Hash256 {
	return n.finalizedRoot
}

// StateDB returns the node's state database (for external access).
func (n *Node) StateDB() *state.StateDB {
	return n.stateDB
}

func (n *Node) persistFinalized() {
	if n.store == nil {
		return
	}
	n.store.PutFinalizedHeight(n.finalizedHeight)
	n.store.PutFinalizedRoot(n.finalizedRoot)
}
