package crypto

import (
	"fmt"

	"github.com/djkazic/tylium/pkg/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// RecoverAddress recovers the signer's address from a compact 64-byte
// signature and the signing hash. It tries both recovery IDs and returns
// the first successfully recovered address.
func RecoverAddress(hash types.Hash256, sig [64]byte) (types.Address, error) {
	// Build 65-byte recoverable signature: [recoveryFlag | R(32) | S(32)]
	// Recovery flags: 27+4 and 28+4 (compressed pubkey variants).
	compact := make([]byte, 65)
	copy(compact[1:33], sig[:32])  // R
	copy(compact[33:65], sig[32:]) // S

	for _, flag := range []byte{27 + 4, 28 + 4} {
		compact[0] = flag
		pub, _, err := ecdsa.RecoverCompact(compact, hash[:])
		if err != nil {
			continue
		}
		addr := types.PubKeyToAddress(pub.SerializeCompressed())
		return addr, nil
	}

	return types.ZeroAddress, fmt.Errorf("signature recovery failed")
}

// VerifySender recovers the signer from a compact signature and checks
// that the derived address matches the claimed sender.
func VerifySender(hash types.Hash256, sig [64]byte, claimed types.Address) bool {
	compact := make([]byte, 65)
	copy(compact[1:33], sig[:32])
	copy(compact[33:65], sig[32:])

	for _, flag := range []byte{27 + 4, 28 + 4} {
		compact[0] = flag
		pub, _, err := ecdsa.RecoverCompact(compact, hash[:])
		if err != nil {
			continue
		}
		addr := types.PubKeyToAddress(pub.SerializeCompressed())
		if addr == claimed {
			return true
		}
	}
	return false
}

// Sign produces a DER-encoded ECDSA signature, stored in a fixed 64-byte
// compact r||s form for Tylium's wire format.
func Sign(hash types.Hash256, key *PrivateKey) ([64]byte, error) {
	sig := ecdsa.Sign(key.key, hash[:])
	if sig == nil {
		return [64]byte{}, fmt.Errorf("signing failed")
	}

	// Serialize to compact r||s (64 bytes).
	compact := sig.Serialize() // DER encoding

	// We'll store DER in the first bytes, zero-padded.
	// For proper r||s, we need to parse the DER.
	var result [64]byte
	parsed, err := parseDERToCompact(compact)
	if err != nil {
		return [64]byte{}, err
	}
	copy(result[:], parsed)
	return result, nil
}

// Verify checks a 64-byte compact ECDSA signature against a public key and message hash.
func Verify(hash types.Hash256, sig [64]byte, pub *PublicKey) bool {
	// Convert compact r||s back to DER for verification.
	derSig := compactToDER(sig[:32], sig[32:])
	parsed, err := ecdsa.ParseDERSignature(derSig)
	if err != nil {
		return false
	}
	return parsed.Verify(hash[:], pub.key)
}

// parseDERToCompact extracts r and s from a DER signature into 64 bytes (32+32).
func parseDERToCompact(der []byte) ([]byte, error) {
	if len(der) < 8 {
		return nil, fmt.Errorf("DER too short")
	}
	// DER: 0x30 <len> 0x02 <rlen> <r> 0x02 <slen> <s>
	pos := 2 // skip 0x30 <len>
	if der[pos] != 0x02 {
		return nil, fmt.Errorf("expected 0x02 for r")
	}
	pos++
	rLen := int(der[pos])
	pos++
	r := der[pos : pos+rLen]
	pos += rLen

	if der[pos] != 0x02 {
		return nil, fmt.Errorf("expected 0x02 for s")
	}
	pos++
	sLen := int(der[pos])
	pos++
	s := der[pos : pos+sLen]

	result := make([]byte, 64)
	// Strip leading zero bytes (sign byte in DER).
	if len(r) > 32 {
		r = r[len(r)-32:]
	}
	if len(s) > 32 {
		s = s[len(s)-32:]
	}
	copy(result[32-len(r):32], r)
	copy(result[64-len(s):64], s)
	return result, nil
}

// compactToDER converts 32-byte r and 32-byte s to DER format.
func compactToDER(r, s []byte) []byte {
	// Strip leading zeros.
	r = stripLeadingZeros(r)
	s = stripLeadingZeros(s)

	// Add sign byte if high bit set.
	if len(r) > 0 && r[0]&0x80 != 0 {
		r = append([]byte{0x00}, r...)
	}
	if len(s) > 0 && s[0]&0x80 != 0 {
		s = append([]byte{0x00}, s...)
	}

	totalLen := 2 + len(r) + 2 + len(s)
	der := make([]byte, 0, 2+totalLen)
	der = append(der, 0x30, byte(totalLen))
	der = append(der, 0x02, byte(len(r)))
	der = append(der, r...)
	der = append(der, 0x02, byte(len(s)))
	der = append(der, s...)
	return der
}

func stripLeadingZeros(b []byte) []byte {
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}
