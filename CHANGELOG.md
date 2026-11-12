# Changelog

## [v0.0.27](https://github.com/flare-foundation/tee-proxy/tree/v0.0.27) - 2026-11-12

### Changed

- tee-node bumped to v0.0.30 and go-flare-common to v1.6.0, which require Go 1.27.4; Go bumped to 1.27.4.

## [v0.0.26](https://github.com/flare-foundation/tee-proxy/tree/v0.0.26) - 2026-11-04

### Added

- `[chain] source = "rpc"` reads on-chain data from a C-chain node's JSON-RPC endpoint instead of the C-chain indexer database; the indexer stays the default.
- `[chain] api_key` sends the endpoint's API key as the `x-apikey` query parameter, as the C-chain indexer does.
  The indexer's environment variables `NODE_URL` and `NODE_API_KEY` override `url` and `api_key`.
- `[chain]` tunes the rpc source with `url_variable`, `api_key_variable`, `max_logs_range` (default 30 blocks per `eth_getLogs`), `max_batch_size` (default 50 calls per batch) and `timeout` (default `10s` per request).
- `[logging] format` selects `"console"` (default) or `"json"` output.

### Changed

- Readiness fails when reading the chain head takes over 2 s, with either chain source.
- `teeproxy_cchain_indexer_delay_seconds` and the `policy_update_total` reason `indexer` now cover either chain source; the names are kept for dashboard compatibility.
- go-flare-common bumped to v1.5.0 and tee-node to v0.0.29.
- Go bumped to 1.26.10; the builder image is pinned by digest.

### Deprecated

- **Behavioral:** `[logging] file`, `max_file_size`, `max_backups`, `max_age_days` and `console` are accepted but ignored, with an info log at startup; logs go to stdout only, file logging and rotation are gone.

### Fixed

- Machine path lists that involve several governances are no longer rejected by the TEE node when `[governance]` is configured: only the node's governance signatures are forwarded.
  Direct-signature deployments now need `signers` + `threshold` set to get this; `[governance]` was previously documented as Safe-only.
- Signing policy initialization and rollover no longer fail for a voter with several `VoterRegistered` events; the first one that recovers to the voter is used.

### Security

- The runtime image requires `ca-certificates-bundle >= 20261020-r0`.

## [v0.0.25](https://github.com/flare-foundation/tee-proxy/tree/v0.0.25) - 2026-10-01

### Changed

- **Behavioral:** `[voting] history_size` also sets the signing policy depth: a fresh start initializes the policy `history_size - 1` epochs back, a restart backfills the last `history_size` policies.
  By default a fresh start goes 2 epochs back instead of 3 (old `initial_signing_policy_offset`), and a restart backfills 3 policies instead of 2.
- go-flare-common bumped to v1.3.0 and tee-node to v0.0.28.
- Go bumped to 1.26.8; the runtime image moves from `alpine:latest` to `alpine:3.23` pinned by digest.

### Removed

- **Breaking:** `initial_signing_policy_offset` is removed; a config that still sets it fails at boot.
  Set `[voting] history_size` instead.

### Fixed

- A restart no longer fails when earlier signing policies are missing from the relay history or indexer retention; they are skipped with a warning, and only a missing latest policy is fatal.
- A restart no longer loads the wrong policy for reward epoch 0.
- VRF instructions whose declared cosigners or threshold do not match the wallet configuration are rejected with HTTP 400 (reason `invalid_cosigner_declaration`), as the TEE node does.
- Instructions declaring the same cosigner twice are rejected with HTTP 400 (`instructions_rejected_total` reason `duplicate_cosigners`), as the TEE node does.
- Restore instructions are rejected with HTTP 400 (reason `invalid_backup_metadata`) when the backup metadata breaks a rule the TEE node enforces: member counts, duplicate admins or cosigners, or a cosigner threshold above the cosigner count.

### Security

- grpc bumped to v1.83.2 and klauspost/compress to v1.18.7 for reported vulnerabilities.

## [v0.0.24](https://github.com/flare-foundation/tee-proxy/tree/v0.0.24) - 2026-08-31

### Changed

- **Breaking:** FDC data-provider signatures and signing-policy rollover signatures are checked against the source-bound Relay digest and policy hash, which include the chain ID.
  Requires the source-bound Relay and a matching tee-node (tee-node pinned to `v0.0.26-0.20260825074205-e0818f8a6375`).
- **Behavioral:** a data-provider threshold override rounds down, as the Relay and the TEE node do; it previously demanded one weight unit more when the product was not exact.
- go-flare-common bumped to `v1.2.2-0.20260820114858-51165807a89f`.

### Fixed

- Instructions for a round whose signing policy has a zero threshold are rejected (`instructions_rejected_total` reason `zero_policy_threshold`) instead of finalizing on any single vote.
- Waiting for a TEE node response no longer misses a result stored while the Redis subscription was being set up.

## [v0.0.23](https://github.com/flare-foundation/tee-proxy/tree/v0.0.23) - 2026-08-19

### Added

- `[info_timing] max_attempts` (default 3) and `retry_delay` (default `5s`) retry a failed TEE info refresh; `cycle_queue_response_wait` is now per attempt.
  The bootstrap fetch retries too, bounded by `initial_timeout`; signature and attestation verification failures are never retried.
- `teeproxy_info_refresh_exhausted_total{stage}` counts refreshes the retries did not recover; `TeeProxyInfoRefreshFailing` now alerts on it.
- `teeproxy_action_redelivery_total{queue,result}` counts undelivered actions put back on their queue, with alerts `TeeProxyActionRedelivering` and `TeeProxyActionRedeliveryLost`.

### Changed

- **Behavioral:** `teeproxy_info_refresh_failures_total` and `teeproxy_info_refresh_duration_seconds` count attempts, not refreshes.
- **Breaking:** `teeproxy_results_discarded_total` is renamed to `teeproxy_results_missing_action_id_total`, and the info refresh `stage` label `signing_hash` to `payload_hash`; alert rules and dashboards on the old names stop matching.

### Fixed

- Actions the TEE node did not receive (poll ended or response write failed) are requeued for the next poll, up to 5 attempts, instead of lost.
  `finalized_action_lost_total{reason="send_failed"}` now counts only main-queue actions given up after those attempts or not requeued.
