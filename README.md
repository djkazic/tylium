# Tylium

A consensus layer anchored to Bitcoin that derives state from witness data embedded in Bitcoin transactions. Tylium uses a deterministic VM, account-based state model, and Merkle-committed checkpoints — all reconstructable from Bitcoin L1 alone.

## Architecture

- **14-opcode stack VM** — uint64 values, gas-metered, deterministic execution
- **Sparse Merkle Tree** — 256-bit depth, global state commitment
- **Witness data embedding** — Taproot script-path witness payloads (segwit discount), OP_RETURN for deposits. [BIP-110](https://bip110.org/) compliant: witness stack items ≤80 bytes, no OP_IF/OP_NOTIF/OP_SUCCESS, single-leaf control block
- **Deposit burn bridge** — deposits burn real BTC via OP_RETURN output value
- **Deterministic ordering** — gas price > priority > txid
- **Near-collision mining** — XOR distance to state root, miners are witnesses only
- **Finality via attestation** — optimistic execution with miner checkpoint finalization
- **LevelDB persistence** — prefix-namespaced storage, full state reconstruction from L1
- **Script language** — text assembly for writing contracts without Go

## Quick Start

### Regtest (local development)

```bash
docker compose up -d
```

This starts Bitcoin Core in regtest mode, `tyld`, and `tylminer` connected to it.

### Testnet

```bash
make build
./tyld --genesis <ACTIVATION_HEIGHT> --btcrpc http://127.0.0.1:18332 \
       --btcuser <user> --btcpass <pass> --datadir ~/.tylium-testnet
./tylminer --btcrpc http://127.0.0.1:18332 --btcuser <user> --btcpass <pass> \
           --tylrpc http://127.0.0.1:19332 --key <PRIVATE_KEY_HEX>
```

Requires a synced Bitcoin Core testnet node with `txindex=1`.

### Create a key and bridge some Bitcoin

```bash
tylcli keygen --name alice
tylcli deposit alice 10000000    # burns 0.1 BTC on L1, credits 10M sats on Tylium
tylcli status <txid>             # check transaction status
tylcli balance alice             # shows finalized + unfinalized balance
```

### Deploy a token and mint

```bash
tylcli deploy token --name mytoken --max-supply 21000000
tylcli call mytoken mint 1000000
tylcli history --limit 5         # recent transactions with finality status
```

### Deploy an AMM pool and swap

```bash
tylcli deploy amm 997 --token-a <token_addr> --name pool
tylcli call pool addLiquidity 100000 100000
tylcli call pool swapAForB 10000
```

All commands return a txid. Use `tylcli status <txid>` to check results and finality. Nonces are auto-fetched, transactions are signed and broadcast to Bitcoin automatically.

## CLI Reference

```
tylcli deposit <to> <amount>             Bridge sats from Bitcoin to Tylium
tylcli deploy token [--name N]           Deploy a token contract
tylcli deploy amm [fee] [--name N]       Deploy an AMM pool (default: 0.3% fee)
tylcli call <contract> <fn> [args...]    Call a contract function
tylcli status <txid>                     Check transaction status and finality
tylcli history [--limit N]               Show recent transactions (default: 20)
tylcli keygen [--name N]                 Generate a keypair
tylcli keys                              List saved keys
tylcli contracts                         List saved contracts
tylcli balance <name|addr>               Get balance (shows finality status)
tylcli account <name|addr>               Nonce, balance, contract status
tylcli finalized                         Show finalized vs optimistic height
tylcli receipts [height]                 Show receipts for a block
tylcli storage <addr> <slot>             Read contract storage
tylcli swap buy --amount <amt>           Buy tyBTC with Lightning (fully automatic)
tylcli swap buy --resume <htlc_id>       Resume an interrupted buy
tylcli swap sell --htlc <id> --invoice   Sell tyBTC for Lightning
tylcli swap list                         List active swaps
tylcli swap status <id>                  Check swap status
tylcli htlc lock <to> <tl> <hash> -v    Lock tyBTC in an HTLC
tylcli htlc claim <id> <preimage>        Claim an HTLC (gasless)
tylcli htlc refund <id>                  Refund an expired HTLC
tylcli htlc query <id>                   Query HTLC status
tylcli tx [--to ...] [--data ...]        Raw transaction builder (power user)
```

### Contract Functions

Token: `mint <amount>`, `transfer <to_id> <amount>`, `balanceOf <addr_id>`

AMM: `addLiquidity <amountA> <amountB>`, `removeLiquidity <lpAmount>`, `swapAForB <amount>`, `swapBForA <amount>`

### Flags

```
--from <name>        Signing key (auto-detected if only one key)
--no-push            Print envelope hex instead of broadcasting
--rpc URL            Tylium RPC (default: http://127.0.0.1:19332)
--btcrpc URL         Bitcoin RPC (default: http://127.0.0.1:18332)
--btcuser USER       Bitcoin RPC user (default: tylium)
--btcpass PASS       Bitcoin RPC pass (default: tylium)
--wallet NAME        Bitcoin wallet name
--swapd URL          Swap daemon API (default: http://127.0.0.1:19333)
--swapd-key KEY      API key for swap daemon
```

Environment variables: `TYL_RPC`, `BTC_RPC`, `BTC_USER`, `BTC_PASS`, `BTC_WALLET`, `TYL_SWAPD`, `TYL_SWAPD_KEY`.

## Building

```bash
make build    # builds all binaries: tyld, tylminer, tylcli, tylpush, tylbuild, tylsim, tylswapd
make test     # runs all tests
make clean    # removes built binaries
```

## Running Tests

```bash
make test                                        # all tests
go test ./contracts -v -count=1                  # contract + AMM validation
go test ./test/stress -v -count=1 -timeout 120s  # stress tests
cd test/e2e && bash run.sh                       # docker e2e
```

## AMM Pools

Constant-product AMM (x * y = k) with configurable swap fees and input validation.

### Fee Tiers

| Fee | Numerator |
|-----|-----------|
| 0.1% | 999 |
| 0.3% | 997 (Uniswap v2) |
| 0.5% | 995 |
| 1.0% | 990 |

### Storage Layout

| Slot | Value |
|------|-------|
| 0 | Reserve of token A |
| 1 | Reserve of token B |
| 2 | Total LP token supply |
| 5 | Fee numerator |
| 1000+addr | LP balance per address |
| 200-201 | Return values |

### Validation

All AMM operations include input validation:
- **addLiquidity**: rejects zero amounts
- **removeLiquidity**: rejects zero LP amount, rejects if LP amount exceeds caller's balance
- **swap**: rejects zero input, rejects swaps on empty pools, rejects swaps that produce zero output (dust)

Invalid operations revert state — the sender pays gas but no reserves change.

### Swap Formula

```
fee = SLOAD(slot 5)
amountOut = reserveOut * (amountIn * fee) / (reserveIn * 1000 + amountIn * fee)
```

## Lightning Atomic Swaps

Tylium supports trustless atomic swaps between Lightning BTC and tyBTC via hash time-locked contracts (HTLCs). This is the primary mechanism for entering and exiting the network.

### Entering: Buy tyBTC with Lightning

```bash
tylcli swap buy --amount 50000    # generates preimage, creates HTLC, prints invoice
```

The CLI handles everything: generates a preimage, calls the swap daemon to lock tyBTC in an HTLC, prints a Lightning invoice to pay, then polls and auto-claims the HTLC once the invoice is paid. HTLC claims are gasless — new accounts with zero balance can claim. If interrupted, resume with `--resume <htlc_id>`.

### Exiting: Sell tyBTC for Lightning

```bash
tylcli htlc lock <daemon_addr> <timelock> <hash> --value 50000
tylcli swap sell --htlc <id> --invoice <bolt11>
```

Lock tyBTC in an HTLC addressed to the swap daemon, then tell the daemon to pay your Lightning invoice. The daemon pays the invoice (revealing the preimage) and claims the HTLC. To precisely empty an account, set `value = balance - 200` with `gasPrice=1, gasLimit=200` — the HTLC lock costs exactly 200 gas.

### Swap Daemon

```bash
tylswapd --key <PRIVATE_KEY_HEX> --datadir ~/.tylswapd \
         --btcrpc http://127.0.0.1:18332 --btcuser <user> --btcpass <pass> \
         --lndrest https://127.0.0.1:8080 --lndcert ~/.lnd/tls.cert \
         --lndmacaroon ~/.lnd/data/chain/bitcoin/testnet/admin.macaroon
```

The daemon manages liquidity, creates hold invoices, monitors HTLCs, and handles refunds. Optional `--apikey` flag enables Bearer token authentication. Swap state is persisted to disk and survives restarts.

### Low-Level HTLC Commands

```bash
tylcli htlc lock <to> <timelock> <hash> --value <amount>
tylcli htlc claim <id> <preimage>
tylcli htlc refund <id>
tylcli htlc query <id>
```

## Call Data ABI

Arguments are packed as 8-byte big-endian uint64 values. The executor maps them to storage slots:

| Slot | Purpose |
|------|---------|
| 100 | Function selector |
| 101 | Arg 1 |
| 102 | Arg 2 |
| 103 | Caller ID (set by executor) |
| 200+ | Return values |

## Script Language

Text assembly for writing contracts without Go. See `contracts/script/`.

```asm
.const SLOT_SUPPLY 0
.const FN_MINT 1

PUSH 100
SLOAD
PUSH FN_MINT
EQ
PUSH @mint
JUMPI
HALT

mint:
  PUSH SLOT_SUPPLY
  SLOAD
  PUSH 101
  SLOAD
  ADD
  PUSH SLOT_SUPPLY
  SSTORE
  HALT
```

Features: named constants (`.const`), labels (`name:`), label references (`PUSH @name`), comments (`;`), hex values, all 14 opcodes, case insensitive.

## Simulation

```bash
go run ./cmd/tylsim/ -duration 1h
go run ./cmd/tylsim/ -accounts 50 -pools 5
```

## Project Structure

```
cmd/
  tyld/          Full node daemon
  tylminer/      Miner daemon
  tylcli/        CLI client (keys, deploy, call, query)
  tylpush/       Low-level envelope embedding tool
  tylbuild/      Contract bytecode generator
  tylswapd/      Lightning swap daemon
  tylsim/        Long-running simulation

contracts/       Token + AMM contract builders & tests
  script/        Text assembly compiler
internal/
  executor/      Block execution pipeline
  mempool/       Transaction ordering
  miner/         Near-collision mining
  node/          Full node orchestration
  rpc/           JSON-RPC server
  scanner/       L1 witness data extraction
  state/         StateDB with Merkle commitment
  store/         LevelDB persistence
  swap/          Atomic swap daemon (HTLC lifecycle, LND integration)
  vm/            Stack-based virtual machine

pkg/
  bitcoin/       Bitcoin Core RPC client
  crypto/        secp256k1 keys & ECDSA signatures
  encoding/      Deterministic binary codec & envelope format
  merkle/        Sparse Merkle Tree (memory + LevelDB backends)
  types/         Core types (Hash256, Address, Account, Transaction, Block)

test/
  e2e/           Docker-compose integration test
  stress/        Stress / fuzz tests
```

## Design Decisions

- **Witness data payloads**: Segwit discount makes witness embedding ~4x cheaper per byte. Large envelopes are split into ≤80-byte witness stack items via taproot script-path spends. Small envelopes (deposits) use OP_RETURN (≤83-byte scriptPubKey). Both paths are [BIP-110](https://bip110.org/) compliant — no OP_IF/OP_NOTIF, no OP_SUCCESS, witness items well under the 256-byte limit, single-leaf control block (33 bytes).
- **Deposit burn bridge**: Deposits burn real BTC by setting the OP_RETURN output value. The extractor credits `min(declared_amount, burned_value)` — you can't mint tyBTC without burning real BTC.
- **Account model**: Simpler than UTXO for smart contracts; nonce prevents replay.
- **14 opcodes**: Minimal instruction set sufficient for DeFi primitives. No loops (JUMP/JUMPI only forward) keeps execution bounded.
- **Integer math only**: All token and AMM math uses uint64. No floating point, no rounding surprises.
- **Storage-slot ABI**: Contract call data is written to slots 100-104 by the executor before VM execution. Return values read from slots 200+. Simple, no ABI encoding needed.
- **Configurable fees**: AMM fee numerator stored in contract storage, not hardcoded. Different pools can have different fee tiers.
- **On-disk key management**: Private keys stored in `~/.tylium/keys/` with named references. Keygen rejects duplicate names.
- **Miners as witnesses**: Miners cannot reorder or censor transactions. Ordering is deterministic from the transaction fields themselves. Miners attest to state roots via near-collision proofs, and the winning miner (lowest XOR distance) receives gas fees.
- **Optimistic execution with finality**: Blocks execute immediately when L1 data arrives (optimistic). Finalized height only advances when a valid miner checkpoint attests to a known state root. Balances show both confirmed and pending amounts.
- **L1 reorg safety**: Detects Bitcoin chain reorganizations by comparing block parent hashes against stored history. On reorg, walks back to the fork point, rolls back state, and reprocesses divergent blocks.
- **Signature verification**: ECDSA recovery (`RecoverCompact`) derives the signer's address from the signature and rejects transactions with invalid or mismatched signatures before execution.
- **Merkle tree snapshots**: Immutable persistent data structure — `TakeSnapshot()` captures the root node for zero-cost read-only historical queries. Used to serve finalized balances without duplicating state.
