package miner

import (
	"context"
	"testing"
	"time"

	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/types"
)

func TestMineProducesCheckpoint(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	m := New(key)
	stateRoot := types.Sha256([]byte("test state"))
	prevCheck := types.Sha256([]byte("prev checkpoint"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cp, err := m.Mine(ctx, stateRoot, prevCheck)
	if err != nil {
		t.Fatal(err)
	}

	if cp.StateRoot != stateRoot {
		t.Fatal("checkpoint state root mismatch")
	}
	if cp.PrevChecksum != prevCheck {
		t.Fatal("checkpoint prev checksum mismatch")
	}
	if cp.AttestationHash.IsZero() {
		t.Fatal("attestation hash should be non-zero")
	}

	// Verify the attestation hash is H(nonce || stateRoot).
	var preimage []byte
	preimage = append(preimage, cp.MinerNonce[:]...)
	preimage = append(preimage, stateRoot[:]...)
	expected := types.Sha256(preimage)
	if cp.AttestationHash != expected {
		t.Fatal("attestation hash does not match H(nonce||stateRoot)")
	}
}

func TestCompareAttestations(t *testing.T) {
	key1, _ := crypto.GenerateKey()
	key2, _ := crypto.GenerateKey()

	stateRoot := types.Sha256([]byte("state"))
	prev := types.ZeroHash

	ctx1, cancel1 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel1()
	m1 := New(key1)
	cp1, err := m1.Mine(ctx1, stateRoot, prev)
	if err != nil {
		t.Fatal(err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	m2 := New(key2)
	cp2, err := m2.Mine(ctx2, stateRoot, prev)
	if err != nil {
		t.Fatal(err)
	}

	// CompareAttestations should not panic and should return a deterministic result.
	result := CompareAttestations(cp1, cp2, stateRoot)
	_ = result // just verify it doesn't panic
}

func TestDistance(t *testing.T) {
	a := types.Sha256([]byte("a"))
	b := types.Sha256([]byte("b"))

	d := Distance(a, b)
	if d.Sign() == 0 {
		t.Fatal("distance between different hashes should be non-zero")
	}

	// Distance to self should be zero.
	d = Distance(a, a)
	if d.Sign() != 0 {
		t.Fatal("distance to self should be zero")
	}
}
