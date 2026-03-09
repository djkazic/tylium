package types

// Block represents a Tylium block, mapped 1:1 to a Bitcoin block.
// Blocks are deterministic artifacts: given L1 block N, scan its txs,
// extract Tylium payloads, order them, execute against state N-1, produce state N.
type Block struct {
	Height        uint64         // L1 Bitcoin block height
	L1BlockHash   Hash256        // Bitcoin block hash (anchor)
	PrevStateRoot Hash256        // state root BEFORE this block's transactions
	StateRoot     Hash256        // state root AFTER this block's transactions
	TxRoot        Hash256        // Merkle root of ordered transaction list
	ReceiptRoot   Hash256        // Merkle root of execution receipts
	Transactions  []*Transaction // deterministically ordered
	GasUsed       uint64         // total gas consumed
	Coinbase      Address        // miner address that receives gas fees
}
