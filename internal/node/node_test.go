package node

import (
	"encoding/json"
	"testing"

	"github.com/djkazic/tylium/internal/executor"
	"github.com/djkazic/tylium/internal/miner"
	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/internal/store"
	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

func testKey(t *testing.T, seed byte) (*crypto.PrivateKey, types.Address) {
	t.Helper()
	var keyBytes [32]byte
	keyBytes[31] = seed
	if seed == 0 {
		keyBytes[31] = 0xff
	}
	key, err := crypto.PrivateKeyFromBytes(keyBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	return key, key.Public().Address()
}

// newTestNode builds a Node backed by a real LevelDB store in a temp dir,
// with an in-memory StateDB and executor — no Bitcoin client needed.
func newTestNode(t *testing.T, genesis uint64) *Node {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	stateDB := state.NewStateDB(merkle.NewMemStore())
	exec := executor.New(stateDB)

	return &Node{
		config:       &Config{GenesisHeight: genesis},
		store:        st,
		stateDB:      stateDB,
		executor:     exec,
		lastHeight:   genesis,
		rootToHeight: make(map[types.Hash256]uint64),
	}
}

// processAndPersist runs executor.ProcessBlock and persists the result,
// updating the node's internal state just like Follow/processBlock do.
// It sets a unique balance per height to ensure distinct state roots.
func processAndPersist(t *testing.T, n *Node, height uint64, txs []*types.Transaction) *types.Block {
	t.Helper()
	// Set a unique balance so each block produces a distinct state root.
	marker := types.BytesToAddress([]byte{0xDE, 0xAD})
	n.stateDB.SetBalance(marker, height*1000)

	l1Hash := types.Sha256([]byte{byte(height)})
	block, receipts, err := n.executor.ProcessBlock(height, l1Hash, txs)
	if err != nil {
		t.Fatal(err)
	}
	n.lastHeight = block.Height
	n.stateRoot = block.StateRoot
	n.rootToHeight[block.StateRoot] = block.Height
	n.persist(block, receipts)
	n.store.PutL1Hash(height, l1Hash)
	return block
}

// makeCheckpoint creates a valid signed checkpoint attesting to the given state root.
func makeCheckpoint(t *testing.T, stateRoot, prevChecksum types.Hash256) *types.Checkpoint {
	t.Helper()
	key, _ := testKey(t, 1)
	m := miner.New(key)

	// Use a short deterministic "mine": just one nonce.
	var nonce [32]byte
	nonce[0] = 0x42
	var preimage []byte
	preimage = append(preimage, nonce[:]...)
	preimage = append(preimage, stateRoot[:]...)
	attestHash := types.Sha256(preimage)

	pub := key.Public()
	cp := &types.Checkpoint{
		StateRoot:       stateRoot,
		PrevChecksum:    prevChecksum,
		MinerNonce:      nonce,
		AttestationHash: attestHash,
	}
	copy(cp.MinerPubKey[:], pub.CompressedBytes())

	// Sign using the same method as the miner.
	sigHash := checkpointSigningHash(cp)
	sig, err := crypto.Sign(sigHash, key)
	if err != nil {
		t.Fatal(err)
	}
	cp.Signature = sig

	// Sanity: the node should accept this checkpoint.
	_ = m // keep reference so import isn't unused
	return cp
}

func TestRollbackRebuildsRootToHeight(t *testing.T) {
	n := newTestNode(t, 0)

	// Process 5 blocks, each producing a unique state root.
	blocks := make([]*types.Block, 5)
	for i := 0; i < 5; i++ {
		blocks[i] = processAndPersist(t, n, uint64(i+1), nil)
	}

	if n.lastHeight != 5 {
		t.Fatalf("expected lastHeight 5, got %d", n.lastHeight)
	}

	// Verify all 5 roots are in rootToHeight.
	for i, b := range blocks {
		h, ok := n.rootToHeight[b.StateRoot]
		if !ok {
			t.Fatalf("block %d state root not in rootToHeight before rollback", i+1)
		}
		if h != uint64(i+1) {
			t.Fatalf("block %d: rootToHeight=%d, want %d", i+1, h, i+1)
		}
	}

	// Roll back to block 2. Blocks 3-5 are gone, but 1-2 should remain.
	if err := n.rollbackToHeight(2); err != nil {
		t.Fatal(err)
	}

	if n.lastHeight != 2 {
		t.Fatalf("after rollback: expected lastHeight 2, got %d", n.lastHeight)
	}

	// Roots for blocks 1 and 2 must still be in rootToHeight.
	for i := 0; i < 2; i++ {
		h, ok := n.rootToHeight[blocks[i].StateRoot]
		if !ok {
			t.Fatalf("after rollback: block %d state root missing from rootToHeight", i+1)
		}
		if h != uint64(i+1) {
			t.Fatalf("after rollback: block %d rootToHeight=%d, want %d", i+1, h, i+1)
		}
	}

	// Roots for blocks 3-5 should NOT be in rootToHeight.
	for i := 2; i < 5; i++ {
		if _, ok := n.rootToHeight[blocks[i].StateRoot]; ok {
			t.Fatalf("after rollback: block %d state root should not be in rootToHeight", i+1)
		}
	}
}

func TestRollbackClearsLastCheckpoint(t *testing.T) {
	n := newTestNode(t, 0)

	// Process a block and set a fake lastCheckpoint.
	processAndPersist(t, n, 1, nil)
	n.lastCheckpoint = &types.Checkpoint{StateRoot: types.Sha256([]byte("fake"))}

	if err := n.rollbackToHeight(0); err != nil {
		t.Fatal(err)
	}
	if n.lastCheckpoint != nil {
		t.Fatal("genesis rollback should clear lastCheckpoint")
	}

	// Non-genesis rollback.
	processAndPersist(t, n, 1, nil)
	processAndPersist(t, n, 2, nil)
	n.lastCheckpoint = &types.Checkpoint{StateRoot: types.Sha256([]byte("fake2"))}

	if err := n.rollbackToHeight(1); err != nil {
		t.Fatal(err)
	}
	if n.lastCheckpoint != nil {
		t.Fatal("non-genesis rollback should clear lastCheckpoint")
	}
}

func TestFinalizationAfterRollback(t *testing.T) {
	n := newTestNode(t, 0)

	// Process blocks 1-5.
	blocks := make([]*types.Block, 5)
	for i := 0; i < 5; i++ {
		blocks[i] = processAndPersist(t, n, uint64(i+1), nil)
	}

	// Finalize block 3 via a checkpoint that attests to block 3's state root.
	cp := makeCheckpoint(t, blocks[2].StateRoot, types.ZeroHash)
	n.processCheckpoints([]*types.Checkpoint{cp})
	n.tryFinalize()

	if n.finalizedHeight != 3 {
		t.Fatalf("expected finalized 3, got %d", n.finalizedHeight)
	}

	// Simulate reorg: roll back to block 2 (below finalized height).
	if err := n.rollbackToHeight(2); err != nil {
		t.Fatal(err)
	}

	if n.finalizedHeight != 2 {
		t.Fatalf("after rollback past finalized: expected finalized 2, got %d", n.finalizedHeight)
	}
	if n.lastCheckpoint != nil {
		t.Fatal("lastCheckpoint should be nil after rollback")
	}

	// Re-process blocks 3-5 (simulating the new chain).
	for i := 3; i <= 5; i++ {
		processAndPersist(t, n, uint64(i), nil)
	}

	// Now submit a checkpoint for block 4's state root (from the new chain).
	cp2 := makeCheckpoint(t, blocks[3].StateRoot, cp.AttestationHash)
	n.processCheckpoints([]*types.Checkpoint{cp2})
	n.tryFinalize()

	// The checkpoint attests to block 4's root. Since rootToHeight was rebuilt,
	// it should find the mapping and finalize.
	if n.finalizedHeight < 3 {
		t.Fatalf("finalization should have advanced after rollback+reprocess, got %d", n.finalizedHeight)
	}
}

func TestCheckpointForEarlierRootFinalizesAfterRollback(t *testing.T) {
	// This is the exact scenario from the bug: a checkpoint attests to a state root
	// from a block earlier than the fork point. After rollback, rootToHeight must
	// contain that earlier mapping or finalization silently fails.
	n := newTestNode(t, 0)

	// Process blocks 1-10.
	blocks := make([]*types.Block, 10)
	for i := 0; i < 10; i++ {
		blocks[i] = processAndPersist(t, n, uint64(i+1), nil)
	}

	// Roll back to block 5 (simulating a reorg at block 6).
	if err := n.rollbackToHeight(5); err != nil {
		t.Fatal(err)
	}

	// Re-process blocks 6-8 on the new fork.
	newBlocks := make([]*types.Block, 3)
	for i := 0; i < 3; i++ {
		newBlocks[i] = processAndPersist(t, n, uint64(i+6), nil)
	}

	// A checkpoint in block 8 attests to block 3's state root (well before fork point).
	cp := makeCheckpoint(t, blocks[2].StateRoot, types.ZeroHash)
	n.processCheckpoints([]*types.Checkpoint{cp})
	n.tryFinalize()

	if n.finalizedHeight != 3 {
		t.Fatalf("checkpoint for pre-fork root: expected finalized 3, got %d", n.finalizedHeight)
	}
}

func TestRollbackToGenesis(t *testing.T) {
	n := newTestNode(t, 0)

	processAndPersist(t, n, 1, nil)
	processAndPersist(t, n, 2, nil)

	// Set some finalization state.
	n.finalizedHeight = 1
	n.finalizedRoot = types.Sha256([]byte("root"))
	n.lastCheckpoint = &types.Checkpoint{}

	if err := n.rollbackToHeight(0); err != nil {
		t.Fatal(err)
	}

	if n.lastHeight != 0 {
		t.Fatalf("expected lastHeight 0, got %d", n.lastHeight)
	}
	if n.stateRoot != types.ZeroHash {
		t.Fatal("expected zero state root after genesis rollback")
	}
	if n.lastCheckpoint != nil {
		t.Fatal("expected nil lastCheckpoint after genesis rollback")
	}
	if n.finalizedHeight != 0 {
		t.Fatalf("expected finalized 0, got %d", n.finalizedHeight)
	}
	if len(n.rootToHeight) != 0 {
		t.Fatalf("expected empty rootToHeight, got %d entries", len(n.rootToHeight))
	}
}

func TestRollbackPreservesFinalizedWhenBeforeFork(t *testing.T) {
	n := newTestNode(t, 0)

	// Process blocks 1-5.
	blocks := make([]*types.Block, 5)
	for i := 0; i < 5; i++ {
		blocks[i] = processAndPersist(t, n, uint64(i+1), nil)
	}

	// Finalize block 2.
	n.finalizedHeight = 2
	n.finalizedRoot = blocks[1].StateRoot
	n.persistFinalized()

	// Roll back to block 3 (finalized height 2 is before fork point).
	if err := n.rollbackToHeight(3); err != nil {
		t.Fatal(err)
	}

	// Finalized height should be preserved — it's at block 2, before fork point 3.
	if n.finalizedHeight != 2 {
		t.Fatalf("finalized should be preserved at 2, got %d", n.finalizedHeight)
	}
	if n.finalizedRoot != blocks[1].StateRoot {
		t.Fatal("finalized root should be preserved")
	}
}

func TestProcessCheckpointsValidation(t *testing.T) {
	n := newTestNode(t, 0)
	processAndPersist(t, n, 1, nil)

	// Invalid checkpoint (bad attestation hash).
	badCP := &types.Checkpoint{
		StateRoot:       n.stateRoot,
		AttestationHash: types.Sha256([]byte("wrong")),
	}
	n.processCheckpoints([]*types.Checkpoint{badCP})
	if n.lastCheckpoint != nil {
		t.Fatal("invalid checkpoint should not be accepted")
	}

	// Valid checkpoint.
	goodCP := makeCheckpoint(t, n.stateRoot, types.ZeroHash)
	n.processCheckpoints([]*types.Checkpoint{goodCP})
	if n.lastCheckpoint == nil {
		t.Fatal("valid checkpoint should be accepted")
	}
}

func TestTryFinalizeRequiresRootInMap(t *testing.T) {
	n := newTestNode(t, 0)
	processAndPersist(t, n, 1, nil)

	// Set a checkpoint with a state root that's NOT in rootToHeight.
	unknownRoot := types.Sha256([]byte("unknown"))
	n.lastCheckpoint = &types.Checkpoint{StateRoot: unknownRoot}

	n.tryFinalize()
	if n.finalizedHeight != 0 {
		t.Fatal("tryFinalize should not advance when root not in map")
	}

	// Now add the mapping and retry.
	n.rootToHeight[unknownRoot] = 1
	n.tryFinalize()
	if n.finalizedHeight != 1 {
		t.Fatalf("expected finalized 1, got %d", n.finalizedHeight)
	}
}

func TestRollbackPersistsCorrectly(t *testing.T) {
	n := newTestNode(t, 0)

	processAndPersist(t, n, 1, nil)
	block2 := processAndPersist(t, n, 2, nil)
	processAndPersist(t, n, 3, nil)

	if err := n.rollbackToHeight(2); err != nil {
		t.Fatal(err)
	}

	// Verify persisted state matches rollback.
	h, _ := n.store.GetHeight()
	if h != 2 {
		t.Fatalf("persisted height: expected 2, got %d", h)
	}
	root, _ := n.store.GetStateRoot()
	if root != block2.StateRoot {
		t.Fatal("persisted state root should match block 2")
	}

	// L1 hashes above fork point should be cleaned up.
	hash3, _ := n.store.GetL1Hash(3)
	if !hash3.IsZero() {
		t.Fatal("L1 hash for block 3 should be deleted after rollback")
	}
	hash2, _ := n.store.GetL1Hash(2)
	if hash2.IsZero() {
		t.Fatal("L1 hash for block 2 should be preserved after rollback")
	}
}

func TestRollbackStateRootMatches(t *testing.T) {
	n := newTestNode(t, 0)

	processAndPersist(t, n, 1, nil)
	block2 := processAndPersist(t, n, 2, nil)
	processAndPersist(t, n, 3, nil)

	if err := n.rollbackToHeight(2); err != nil {
		t.Fatal(err)
	}

	if n.stateRoot != block2.StateRoot {
		t.Fatal("stateRoot should match block 2 after rollback")
	}

	// Verify it was loaded from the persisted block data.
	blockData, err := n.store.GetBlock(2)
	if err != nil || blockData == nil {
		t.Fatal("block 2 data should exist in store")
	}
	var stored types.Block
	if err := json.Unmarshal(blockData, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.StateRoot != n.stateRoot {
		t.Fatal("stored block 2 root should match node state root")
	}
}
