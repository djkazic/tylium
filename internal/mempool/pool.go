package mempool

import (
	"sort"

	"github.com/djkazic/tylium/pkg/types"
)

// Pool collects transactions extracted from an L1 block and sorts them
// deterministically for execution.
type Pool struct {
	txs []*types.Transaction
}

func New() *Pool {
	return &Pool{}
}

// Add inserts a transaction into the pool.
func (p *Pool) Add(tx *types.Transaction) {
	p.txs = append(p.txs, tx)
}

// Ordered returns transactions sorted by the deterministic ordering:
// higher gas price first, then higher priority, then lower txid.
func (p *Pool) Ordered() []*types.Transaction {
	sorted := make([]*types.Transaction, len(p.txs))
	copy(sorted, p.txs)
	sort.Slice(sorted, func(i, j int) bool {
		return types.TxLess(sorted[i], sorted[j])
	})
	return sorted
}

// Len returns the number of transactions in the pool.
func (p *Pool) Len() int {
	return len(p.txs)
}

// Clear removes all transactions.
func (p *Pool) Clear() {
	p.txs = p.txs[:0]
}
