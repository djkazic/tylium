package encoding

import (
	"bytes"
	"testing"

	"github.com/djkazic/tylium/pkg/types"
)

func TestTxRoundtrip(t *testing.T) {
	tx := &types.Transaction{
		Version:  1,
		Nonce:    42,
		From:     types.BytesToAddress([]byte{0x01, 0x02, 0x03}),
		To:       types.BytesToAddress([]byte{0x04, 0x05, 0x06}),
		Value:    1000000,
		GasPrice: 100,
		GasLimit: 21000,
		Priority: 5,
		Data:     []byte{0xAA, 0xBB, 0xCC},
	}
	tx.Signature[0] = 0xFF
	tx.Signature[63] = 0x01

	encoded := EncodeTx(tx)
	decoded, err := DecodeTx(encoded)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Version != tx.Version {
		t.Fatalf("version: %d != %d", decoded.Version, tx.Version)
	}
	if decoded.Nonce != tx.Nonce {
		t.Fatalf("nonce: %d != %d", decoded.Nonce, tx.Nonce)
	}
	if decoded.From != tx.From {
		t.Fatalf("from mismatch")
	}
	if decoded.To != tx.To {
		t.Fatalf("to mismatch")
	}
	if decoded.Value != tx.Value {
		t.Fatalf("value: %d != %d", decoded.Value, tx.Value)
	}
	if decoded.GasPrice != tx.GasPrice {
		t.Fatalf("gasPrice: %d != %d", decoded.GasPrice, tx.GasPrice)
	}
	if decoded.GasLimit != tx.GasLimit {
		t.Fatalf("gasLimit: %d != %d", decoded.GasLimit, tx.GasLimit)
	}
	if decoded.Priority != tx.Priority {
		t.Fatalf("priority: %d != %d", decoded.Priority, tx.Priority)
	}
	if !bytes.Equal(decoded.Data, tx.Data) {
		t.Fatalf("data mismatch")
	}
	if decoded.Signature != tx.Signature {
		t.Fatalf("signature mismatch")
	}
}

func TestAccountRoundtrip(t *testing.T) {
	acct := &types.Account{
		Nonce:       10,
		Balance:     999999,
		CodeHash:    types.Sha256([]byte("code")),
		StorageRoot: types.Sha256([]byte("storage")),
	}

	encoded := EncodeAccount(acct)
	decoded, err := DecodeAccount(encoded)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Nonce != acct.Nonce {
		t.Fatalf("nonce: %d != %d", decoded.Nonce, acct.Nonce)
	}
	if decoded.Balance != acct.Balance {
		t.Fatalf("balance: %d != %d", decoded.Balance, acct.Balance)
	}
	if decoded.CodeHash != acct.CodeHash {
		t.Fatalf("codeHash mismatch")
	}
	if decoded.StorageRoot != acct.StorageRoot {
		t.Fatalf("storageRoot mismatch")
	}
}

func TestCheckpointRoundtrip(t *testing.T) {
	cp := &types.Checkpoint{
		StateRoot:    types.Sha256([]byte("state")),
		PrevChecksum: types.Sha256([]byte("prev")),
		TxRoot:       types.Sha256([]byte("txs")),
		GasCollected: 50000,
	}
	cp.MinerPubKey[0] = 0x02
	cp.MinerNonce[0] = 0xAB

	encoded := EncodeCheckpoint(cp)
	decoded, err := DecodeCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.StateRoot != cp.StateRoot {
		t.Fatalf("stateRoot mismatch")
	}
	if decoded.PrevChecksum != cp.PrevChecksum {
		t.Fatalf("prevChecksum mismatch")
	}
	if decoded.GasCollected != cp.GasCollected {
		t.Fatalf("gasCollected: %d != %d", decoded.GasCollected, cp.GasCollected)
	}
}

func TestEnvelopeRoundtrip(t *testing.T) {
	payload := []byte("hello tylium")
	encoded := EncodeEnvelope(EnvelopeTypeTx, payload)
	decoded, err := DecodeEnvelope(encoded)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Type != EnvelopeTypeTx {
		t.Fatalf("type: %d != %d", decoded.Type, EnvelopeTypeTx)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestInvalidEnvelope(t *testing.T) {
	// Too short.
	_, err := DecodeEnvelope([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("expected error for short buffer")
	}

	// Wrong magic.
	bad := make([]byte, 20)
	bad[0] = 'X'
	_, err = DecodeEnvelope(bad)
	if err == nil {
		t.Fatal("expected error for wrong magic")
	}
}
