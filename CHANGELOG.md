# Changelog

## [Unreleased]

### Added

- `[chain] source = "rpc"` reads on-chain data from a C-chain node's JSON-RPC endpoint instead of the C-chain indexer database; the indexer stays the default.
- `[chain] api_key` sends the endpoint's API key as the `x-apikey` query parameter, as the C-chain indexer does.
  The indexer's environment variables `NODE_URL` and `NODE_API_KEY` override `url` and `api_key`.

### Changed

- Readiness fails when reading the chain head takes over 2 s, with either chain source.

### Fixed

- Machine path lists that involve several governances are no longer rejected by the TEE node when `[governance]` is configured: only the node's governance signatures are forwarded.
  Direct-signature deployments now need `signers` + `threshold` set to get this; `[governance]` was previously documented as Safe-only.
