package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/djkazic/tylium/contracts"
)

// tylbuild outputs contract bytecode hex for built-in contract templates.
//
// Usage:
//   tylbuild token
//   tylbuild amm [fee_numerator]

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "token":
		ownerID := uint64(0)
		maxSupply := uint64(0)
		if len(os.Args) >= 3 {
			v, err := strconv.ParseUint(os.Args[2], 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid owner_id: %v\n", err)
				os.Exit(1)
			}
			ownerID = v
		}
		if len(os.Args) >= 4 {
			v, err := strconv.ParseUint(os.Args[3], 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid max_supply: %v\n", err)
				os.Exit(1)
			}
			maxSupply = v
		}
		code := contracts.BuildTokenContract(ownerID, maxSupply)
		fmt.Println(hex.EncodeToString(code))
		fmt.Fprintf(os.Stderr, "owner: %d, max supply: %d\n", ownerID, maxSupply)
	case "amm":
		fee := uint64(997)
		tokenAID := uint64(0)
		if len(os.Args) >= 3 {
			v, err := strconv.ParseUint(os.Args[2], 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid fee: %v\n", err)
				os.Exit(1)
			}
			fee = v
		}
		if len(os.Args) >= 4 {
			v, err := strconv.ParseUint(os.Args[3], 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid token_a_id: %v\n", err)
				os.Exit(1)
			}
			tokenAID = v
		}
		code := contracts.BuildAMMContract(fee, tokenAID)
		fmt.Println(hex.EncodeToString(code))
		fmt.Fprintf(os.Stderr, "fee: %d/1000 (%.1f%% swap fee), tokenA: %d\n", fee, float64(1000-fee)/10, tokenAID)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: tylbuild <contract> [args...]

Contracts:
  token [owner_id] [max_supply]  Fungible token (mint, transfer, balanceOf)
  amm [fee]          AMM pool (default fee: 997 = 0.3%)

Common fee values:
  997 = 0.3%    995 = 0.5%    990 = 1.0%    999 = 0.1%`)
	os.Exit(1)
}
