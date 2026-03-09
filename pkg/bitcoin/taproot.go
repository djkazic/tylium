package bitcoin

import (
	"crypto/sha256"
	"encoding/hex"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// numsInternalKey is a Nothing Up My Sleeve (NUMS) point with no known
// discrete log, making the key-path unspendable. This is the standard
// NUMS point used by Ordinals and other Bitcoin protocols.
var numsInternalKey = func() []byte {
	b, _ := hex.DecodeString("50929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0")
	return b
}()

// taprootTaggedHash computes BIP-340 tagged hash: SHA256(SHA256(tag) || SHA256(tag) || data).
func taprootTaggedHash(tag string, data ...[]byte) [32]byte {
	tagHash := sha256.Sum256([]byte(tag))
	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])
	for _, d := range data {
		h.Write(d)
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// taprootLeafHash computes the BIP-341 tapleaf hash for a tapscript.
func taprootLeafHash(script []byte) [32]byte {
	var buf []byte
	buf = append(buf, 0xc0) // tapscript leaf version
	buf = append(buf, compactSizeEncode(uint64(len(script)))...)
	buf = append(buf, script...)
	return taprootTaggedHash("TapLeaf", buf)
}

// ComputeTaprootOutput computes a P2TR output key for a single tapscript leaf.
// Returns the 34-byte P2TR scriptPubKey and the 33-byte control block.
func ComputeTaprootOutput(script []byte) (p2trScript []byte, controlBlock []byte, err error) {
	leafHash := taprootLeafHash(script)

	// Compute tweak: tagged_hash("TapTweak", internal_key || leaf_hash)
	tweak := taprootTaggedHash("TapTweak", numsInternalKey, leafHash[:])

	// Parse internal key as a public key (even y assumed).
	serialized := make([]byte, 33)
	serialized[0] = 0x02
	copy(serialized[1:], numsInternalKey)
	internalPub, err := secp.ParsePubKey(serialized)
	if err != nil {
		return nil, nil, err
	}

	// Compute tweak*G.
	var tweakScalar secp.ModNScalar
	tweakScalar.SetBytes(&tweak)
	var tweakPoint secp.JacobianPoint
	secp.ScalarBaseMultNonConst(&tweakScalar, &tweakPoint)

	// Compute output_key = internal_key + tweak*G.
	var internalPoint, outputPoint secp.JacobianPoint
	internalPub.AsJacobian(&internalPoint)
	secp.AddNonConst(&internalPoint, &tweakPoint, &outputPoint)
	outputPoint.ToAffine()

	outputPub := secp.NewPublicKey(&outputPoint.X, &outputPoint.Y)
	compressed := outputPub.SerializeCompressed()

	// Build P2TR scriptPubKey: OP_1 <32-byte x-only key>
	p2trScript = make([]byte, 34)
	p2trScript[0] = 0x51 // OP_1 (witness v1)
	p2trScript[1] = 0x20 // push 32 bytes
	copy(p2trScript[2:], compressed[1:33])

	// Build control block: (leaf_version | output_key_parity) || internal_key
	parity := byte(0)
	if compressed[0] == 0x03 {
		parity = 1
	}
	controlBlock = make([]byte, 33)
	controlBlock[0] = 0xc0 | parity
	copy(controlBlock[1:], numsInternalKey)

	return p2trScript, controlBlock, nil
}

// compactSizeEncode returns the Bitcoin compact size encoding of v.
func compactSizeEncode(v uint64) []byte {
	if v < 0xfd {
		return []byte{byte(v)}
	} else if v <= 0xffff {
		return []byte{0xfd, byte(v), byte(v >> 8)}
	} else if v <= 0xffffffff {
		return []byte{0xfe, byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}
	return []byte{0xff, byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24),
		byte(v >> 32), byte(v >> 40), byte(v >> 48), byte(v >> 56)}
}
