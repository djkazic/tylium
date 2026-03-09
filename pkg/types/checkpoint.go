package types

// Checkpoint is the attestation a winning miner submits to L1.
// It anchors the Tylium state to the Bitcoin chain and forms a linked chain
// of state commitments (each checkpoint's PrevChecksum points to the prior).
type Checkpoint struct {
	StateRoot       Hash256  // committed state root after block execution
	PrevChecksum    Hash256  // state root of the previous checkpoint
	TxRoot          Hash256  // Merkle root of txs in this block
	GasCollected    uint64   // total gas fees in this block
	MinerPubKey     [33]byte // compressed pubkey of winning miner
	MinerNonce      [32]byte // nonce that produced the near-collision hash
	AttestationHash Hash256  // H(MinerNonce || StateRoot)
	Signature       [64]byte // miner's signature over the checkpoint
}
