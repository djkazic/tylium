package scanner

import (
	"context"
	"fmt"
	"log"

	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/types"
)

// ScanResult holds the Tylium transactions extracted from a single L1 block.
type ScanResult struct {
	L1Height      uint64
	L1BlockHash   types.Hash256
	L1PrevHash    types.Hash256        // previous L1 block hash (for reorg detection)
	Payloads      []*types.Transaction // decoded Tylium txs in L1 order
	Checkpoints   []*types.Checkpoint  // any checkpoint attestations found
}

// Scanner watches the Bitcoin chain for Tylium payloads.
type Scanner struct {
	btc       bitcoin.Client
	extractor *Extractor
}

// New creates a scanner with the given Bitcoin client.
func New(btc bitcoin.Client) *Scanner {
	return &Scanner{
		btc:       btc,
		extractor: NewExtractor(),
	}
}

// ScanBlock extracts all Tylium payloads from a single L1 block.
func (s *Scanner) ScanBlock(height uint64) (*ScanResult, error) {
	block, err := s.btc.GetBlockByHeight(height)
	if err != nil {
		return nil, fmt.Errorf("scan block %d: %w", height, err)
	}

	txs, checkpoints := s.extractor.ExtractFromBlock(block)

	return &ScanResult{
		L1Height:    block.Height,
		L1BlockHash: block.Hash,
		L1PrevHash:  block.PrevHash,
		Payloads:    txs,
		Checkpoints: checkpoints,
	}, nil
}

// Subscribe returns a channel that emits scan results as new L1 blocks arrive.
func (s *Scanner) Subscribe(ctx context.Context, fromHeight uint64) (<-chan *ScanResult, error) {
	blocks, err := s.btc.SubscribeNewBlocks(ctx, fromHeight)
	if err != nil {
		return nil, err
	}

	results := make(chan *ScanResult, 1)
	go func() {
		defer close(results)
		for block := range blocks {
			txs, checkpoints := s.extractor.ExtractFromBlock(block)
			result := &ScanResult{
				L1Height:    block.Height,
				L1BlockHash: block.Hash,
				L1PrevHash:  block.PrevHash,
				Payloads:    txs,
				Checkpoints: checkpoints,
			}
			select {
			case results <- result:
			case <-ctx.Done():
				return
			}
			log.Printf("scanned block %d: %d txs, %d checkpoints", block.Height, len(txs), len(checkpoints))
		}
	}()

	return results, nil
}
