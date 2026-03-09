package node

// Config holds the node configuration.
type Config struct {
	// BitcoinRPC is the Bitcoin Core JSON-RPC endpoint.
	BitcoinRPC string
	// BitcoinUser is the RPC username.
	BitcoinUser string
	// BitcoinPass is the RPC password.
	BitcoinPass string
	// GenesisHeight is the L1 block height where Tylium begins.
	GenesisHeight uint64
	// DataDir is the directory for persistent state.
	DataDir string
	// RPCAddr is the address for the JSON-RPC server (e.g. ":19332").
	RPCAddr string
}
