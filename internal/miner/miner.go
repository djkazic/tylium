package miner

import (
	"bytes"
	"context"
	"crypto/rand"
	"math/big"

	"github.com/djkazic/tylium/pkg/crypto"
	"github.com/djkazic/tylium/pkg/types"
)

// Miner searches for near-collision hashes to attest to state checkpoints.
// Miners are witnesses — they do not control transaction selection.
type Miner struct {
	privKey *crypto.PrivateKey
}

// New creates a miner with the given private key.
func New(key *crypto.PrivateKey) *Miner {
	return &Miner{privKey: key}
}

// Mine searches for the best nonce such that H(nonce || stateRoot) is as close
// as possible to stateRoot (XOR distance). Runs until ctx is cancelled.
func (m *Miner) Mine(ctx context.Context, stateRoot types.Hash256, prevChecksum types.Hash256) (*types.Checkpoint, error) {
	var bestNonce [32]byte
	var bestHash types.Hash256
	bestDist := new(big.Int)
	// Initialize bestDist to maximum (all 1s).
	bestDist.SetBytes(bytes.Repeat([]byte{0xFF}, 32))
	found := false

	for {
		select {
		case <-ctx.Done():
			if !found {
				return nil, ctx.Err()
			}
			return m.buildCheckpoint(stateRoot, prevChecksum, bestNonce, bestHash)
		default:
		}

		// Generate random nonce.
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			continue
		}

		// Compute attestation hash = SHA256(nonce || stateRoot).
		var preimage []byte
		preimage = append(preimage, nonce[:]...)
		preimage = append(preimage, stateRoot[:]...)
		candidate := types.Sha256(preimage)

		dist := Distance(candidate, stateRoot)
		if dist.Cmp(bestDist) < 0 {
			bestDist = dist
			bestNonce = nonce
			bestHash = candidate
			found = true
		}
	}
}

func (m *Miner) buildCheckpoint(stateRoot, prevChecksum types.Hash256, nonce [32]byte, attestHash types.Hash256) (*types.Checkpoint, error) {
	pub := m.privKey.Public()
	compressed := pub.CompressedBytes()

	cp := &types.Checkpoint{
		StateRoot:       stateRoot,
		PrevChecksum:    prevChecksum,
		MinerNonce:      nonce,
		AttestationHash: attestHash,
	}
	copy(cp.MinerPubKey[:], compressed)

	// Sign the checkpoint.
	sigHash := checkpointSigningHash(cp)
	sig, err := crypto.Sign(sigHash, m.privKey)
	if err != nil {
		return nil, err
	}
	cp.Signature = sig

	return cp, nil
}

func checkpointSigningHash(cp *types.Checkpoint) types.Hash256 {
	var buf []byte
	buf = append(buf, cp.StateRoot[:]...)
	buf = append(buf, cp.PrevChecksum[:]...)
	buf = append(buf, cp.MinerNonce[:]...)
	buf = append(buf, cp.AttestationHash[:]...)
	buf = append(buf, cp.MinerPubKey[:]...)
	return types.DoubleSha256(buf)
}

// Distance computes the XOR distance between two hashes as a big.Int.
func Distance(a, b types.Hash256) *big.Int {
	var xor [32]byte
	for i := range xor {
		xor[i] = a[i] ^ b[i]
	}
	return new(big.Int).SetBytes(xor[:])
}

// CompareAttestations determines the winner between two checkpoint attestations.
// Returns negative if a wins, positive if b wins, zero if tied.
func CompareAttestations(a, b *types.Checkpoint, stateRoot types.Hash256) int {
	distA := Distance(a.AttestationHash, stateRoot)
	distB := Distance(b.AttestationHash, stateRoot)
	cmp := distA.Cmp(distB)
	if cmp != 0 {
		return cmp
	}
	// Tiebreaker: lower pubkey value wins.
	return bytes.Compare(a.MinerPubKey[:], b.MinerPubKey[:])
}
