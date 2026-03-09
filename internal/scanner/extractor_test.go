package scanner

import (
	"testing"

	"github.com/djkazic/tylium/pkg/bitcoin"
	"github.com/djkazic/tylium/pkg/encoding"
	"github.com/djkazic/tylium/pkg/types"
)

func TestExtractFromWitness(t *testing.T) {
	// Create a Tylium transaction, wrap it in an envelope.
	tx := &types.Transaction{
		Version:  1,
		Nonce:    0,
		Value:    1000,
		GasPrice: 10,
		GasLimit: 21000,
	}
	txBytes := encoding.EncodeTx(tx)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeTx, txBytes)

	// Embed the envelope in witness data of a Bitcoin tx.
	block := &bitcoin.Block{
		Height: 100,
		Hash:   types.Sha256([]byte("block")),
		Txs: []bitcoin.Tx{
			{
				TxID: types.Sha256([]byte("tx1")),
				Inputs: []bitcoin.TxInput{
					{Witness: [][]byte{envelope}},
				},
			},
		},
	}

	ext := NewExtractor()
	txs, checkpoints := ext.ExtractFromBlock(block)

	if len(txs) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(txs))
	}
	if len(checkpoints) != 0 {
		t.Fatalf("expected 0 checkpoints, got %d", len(checkpoints))
	}
	if txs[0].Value != 1000 {
		t.Fatalf("expected value 1000, got %d", txs[0].Value)
	}
}

func TestExtractCheckpointFromWitness(t *testing.T) {
	cp := &types.Checkpoint{
		StateRoot:    types.Sha256([]byte("state")),
		PrevChecksum: types.Sha256([]byte("prev")),
		GasCollected: 5000,
	}
	cpBytes := encoding.EncodeCheckpoint(cp)
	envelope := encoding.EncodeEnvelope(encoding.EnvelopeTypeCheckpoint, cpBytes)

	block := &bitcoin.Block{
		Height: 50,
		Txs: []bitcoin.Tx{
			{
				Inputs: []bitcoin.TxInput{
					{Witness: [][]byte{
						[]byte("some other data"),
						envelope,
					}},
				},
			},
		},
	}

	ext := NewExtractor()
	txs, checkpoints := ext.ExtractFromBlock(block)

	if len(txs) != 0 {
		t.Fatalf("expected 0 txs, got %d", len(txs))
	}
	if len(checkpoints) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(checkpoints))
	}
	if checkpoints[0].StateRoot != cp.StateRoot {
		t.Fatalf("checkpoint state root mismatch")
	}
}

func TestExtractIgnoresNonTyliumData(t *testing.T) {
	block := &bitcoin.Block{
		Height: 1,
		Txs: []bitcoin.Tx{
			{
				Inputs: []bitcoin.TxInput{
					{Witness: [][]byte{
						[]byte("random data"),
						[]byte{0x01, 0x02, 0x03},
					}},
				},
			},
		},
	}

	ext := NewExtractor()
	txs, checkpoints := ext.ExtractFromBlock(block)

	if len(txs) != 0 {
		t.Fatalf("expected 0 txs, got %d", len(txs))
	}
	if len(checkpoints) != 0 {
		t.Fatalf("expected 0 checkpoints, got %d", len(checkpoints))
	}
}
