package scanner

import (
	"bytes"

	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/types"
)

// Extractor finds and decodes Tylium payloads from Bitcoin transactions.
// Small payloads (deposits) use OP_RETURN outputs for provable burns.
// Large payloads (deploys, calls) use P2WSH witness embedding.
type Extractor struct {
	magic []byte
}

func NewExtractor() *Extractor {
	return &Extractor{magic: encoding.Magic[:]}
}

// ExtractFromBlock scans all transactions in a Bitcoin block for Tylium
// envelopes in both OP_RETURN outputs and witness data.
func (e *Extractor) ExtractFromBlock(block *bitcoin.Block) ([]*types.Transaction, []*types.Checkpoint) {
	var txs []*types.Transaction
	var checkpoints []*types.Checkpoint

	const maxWitnessSize = 10_000_000 // 10MB per tx witness

	for i := range block.Txs {
		btcTx := &block.Txs[i]
		// Derive a nonce from the Bitcoin txid so each deposit is unique.
		l1Nonce := btcTxNonce(btcTx.TxID)

		// Scan OP_RETURN outputs (small envelopes like deposits).
		for _, output := range btcTx.Outputs {
			if len(output.Script) > 1 && output.Script[0] == 0x6a {
				// OP_RETURN — scan the raw script bytes after the opcode.
				// Pass the output value so deposit envelopes can validate the burn.
				e.processPayloads(e.scanForMagic(output.Script[1:]), output.Value, l1Nonce, &txs, &checkpoints)
			}
		}

		// Scan witness data (large envelopes like deploys and calls).
		for _, input := range btcTx.Inputs {
			// Concatenate all witness items before scanning. This handles
			// envelopes split across multiple stack items when data exceeds
			// the 520-byte P2WSH push limit. The envelope's length field
			// ensures we decode exactly the right bytes.
			totalSize := 0
			for _, item := range input.Witness {
				totalSize += len(item)
			}
			if totalSize > maxWitnessSize {
				continue // skip oversized witness
			}
			combined := make([]byte, 0, totalSize)
			for _, item := range input.Witness {
				combined = append(combined, item...)
			}
			e.processPayloads(e.scanForMagic(combined), 0, l1Nonce, &txs, &checkpoints)
		}
	}

	return txs, checkpoints
}

func (e *Extractor) processPayloads(payloads [][]byte, burnValue uint64, l1Nonce uint64, txs *[]*types.Transaction, checkpoints *[]*types.Checkpoint) {
	for _, payload := range payloads {
		env, err := encoding.DecodeEnvelope(payload)
		if err != nil {
			continue
		}
		switch env.Type {
		case encoding.EnvelopeTypeTx:
			tx, err := encoding.DecodeTx(env.Payload)
			if err == nil {
				*txs = append(*txs, tx)
			}
		case encoding.EnvelopeTypeCheckpoint:
			cp, err := encoding.DecodeCheckpoint(env.Payload)
			if err == nil {
				*checkpoints = append(*checkpoints, cp)
			}
		case encoding.EnvelopeTypeDeposit:
			recipient, amount, err := encoding.DecodeDeposit(env.Payload)
			if err == nil {
				// The credited amount is the minimum of the declared amount
				// and the actual BTC burned in the OP_RETURN output.
				// This prevents minting tyBTC without burning real BTC.
				credited := amount
				if burnValue < credited {
					credited = burnValue
				}
				if credited == 0 {
					continue // no BTC burned, no deposit
				}
				tx := &types.Transaction{
					Version: 1,
					Nonce:   l1Nonce, // derived from L1 txid for uniqueness
					From:    types.ZeroAddress,
					To:      recipient,
					Value:   credited,
				}
				*txs = append(*txs, tx)
			}
		}
	}
}

// btcTxNonce derives a uint64 nonce from a Bitcoin txid (first 8 bytes).
func btcTxNonce(txid types.Hash256) uint64 {
	return uint64(txid[0])<<56 | uint64(txid[1])<<48 | uint64(txid[2])<<40 | uint64(txid[3])<<32 |
		uint64(txid[4])<<24 | uint64(txid[5])<<16 | uint64(txid[6])<<8 | uint64(txid[7])
}

// scanForMagic searches raw bytes for Tylium envelope(s) by scanning
// for the magic prefix.
func (e *Extractor) scanForMagic(data []byte) [][]byte {
	var results [][]byte
	search := data

	for {
		idx := bytes.Index(search, e.magic)
		if idx < 0 {
			break
		}

		// Try to decode an envelope starting at this position.
		candidate := search[idx:]
		env, err := encoding.DecodeEnvelope(candidate)
		if err != nil {
			// Move past this magic occurrence and keep searching.
			search = search[idx+len(e.magic):]
			continue
		}

		// Full envelope length: magic(4) + type(1) + length(4) + payload.
		envLen := 4 + 1 + 4 + len(env.Payload)
		results = append(results, candidate[:envLen])
		search = search[idx+envLen:]
	}

	return results
}
