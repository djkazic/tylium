package types

// Account represents a Tylium account in the global state.
type Account struct {
	Nonce       uint64  // replay protection counter
	Balance     uint64  // balance in photons (smallest unit)
	CodeHash    Hash256 // SHA-256 of contract bytecode; zero for EOA
	StorageRoot Hash256 // Merkle root of account's storage trie
}

// IsContract returns true if the account has deployed code.
func (a *Account) IsContract() bool {
	return !a.CodeHash.IsZero()
}
