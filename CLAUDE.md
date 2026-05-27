# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@GOAI.md

## Project Overview

Flare TEE Proxy is a Go service that acts as an intermediary between external clients and a TEE (Trusted Execution Environment) node for the Flare Network.
It handles instruction submission with threshold-based voting consensus, wallet management, signing policy coordination, and cryptographic operations.

## Setup

```bash
cp config.example.toml config/config.toml
# Edit config/config.toml with your settings
export PRIVATE_KEY=0x...
```

Requires: Redis server, MySQL/SQLite database with C-chain indexer data.

## Architecture

### Package Layout

- **`cmd/proxy/`** — Entry point. Reads config from `./config/config.toml`, loads private key from env var, runs initialization in a goroutine, and listens for OS shutdown signals.
- **`internal/`** — Private business logic (not importable by external packages).
- **`pkg/`** — Reusable public packages (config, storage, status codes, policy, instruction types).
- **`test/`** — Integration tests and shared test utilities.

### Core Services (`internal/service/`)

| Service        | Purpose                                                                                                                           |
| -------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `instruction/` | Processes incoming instructions, manages voting proposals with threshold-based consensus, tracks voting history in cyclic storage |
| `policy/`      | Fetches signing policies from blockchain (Relay contract), validates voter permissions, distributes policies via ActionQueues     |
| `info/`        | Periodically retrieves TEE identity, public keys, and signing policy hashes from the TEE node                                     |
| `wallets/`     | Manages wallet key creation, backup, recovery, and deletion with Redis persistence                                                |
| `result/`      | Stores instruction execution results in Redis, triggers wallet backups                                                            |

### HTTP Servers (`internal/server/`)

- **Internal server** (port 6661): System-facing API for action retrieval, wallet info, liveness checks.
- **External server** (port 6662): Client-facing API for instructions, wallets, TEE info, attestations.

### Key Infrastructure

- **Queue system** (`internal/queue/`): Three queue types (Direct, Main, Backup) backed by Redis. Action bodies are kept under the configured `storage.action_ttl` (default 14 days).
- **Generic storage** (`pkg/storage/`): Redis-backed `Storage[T]` with get/set/queue/pub-sub operations, namespaced keys.
- **Status mapping** (`pkg/status/`): Maps domain errors to HTTP status codes.
- **Liveness** (`internal/liveness/`): Health checks monitoring C-chain indexer and info service delays.

### Data Flow

```
Client → External Server → Instruction Service → Voting Storage → Consensus Check → Action Queue
                                                                                         ↓
Client ← External Server ← Result Storage ←──────────────────────── TEE Node Processor
```

### Key Dependencies

- `github.com/flare-foundation/go-flare-common` — Shared logging, database, utilities
- `github.com/flare-foundation/tee-node` — TEE processor
- `github.com/ethereum/go-ethereum` — Ethereum cryptography and ABI encoding
- `github.com/redis/go-redis/v9` — Redis client for queuing and storage
- `github.com/alicebob/miniredis/v2` — In-memory Redis for tests

## MDs

Every sentence must be in its own line.
