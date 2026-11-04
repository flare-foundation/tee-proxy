<div align="center"> <a href="https://flare.network/" target="blank"> <img src="https://content.flare.network/Flare-2.svg" width="300" alt="Flare Logo" /> </a> <br /> <a href="CONTRIBUTING.md">Contributing</a> · <a href="SECURITY.md">Security</a> · <a href="CHANGELOG.md">Changelog</a> </div>

# Flare TEE proxy

## Running

Copy config.example.toml to config.toml

```bash
cp ./config/config.example.toml ./config/config.toml
```

and set the configurations.

Make sure that the proxy's private key is stored in the environment variable `PRIVATE_KEY`.
If you want it read from a different environment, set specify the name in config under `private_key_variable`

Start the proxy

```bash
go run ./...
```

## Chain Source

The proxy reads signing policies, voter registrations, policy signatures and machine path lists from the C-chain.
`[chain] source` selects where:

- `indexer` (default) reads the C-chain indexer database configured under `[db]`.
- `rpc` reads a C-chain node's JSON-RPC endpoint; no indexer or database is needed.

```toml
[chain]
source = "rpc"
url = "https://coston2-api.flare.network/ext/C/rpc" # or set NODE_URL
max_logs_range = 30 # blocks per eth_getLogs request; public Flare nodes cap it at 30
```

An endpoint that needs credentials can take them in `url` itself, as a key in the path or query or as `user:password@` for basic auth, or in `api_key` (or `NODE_API_KEY`), which is sent as the `x-apikey` query parameter, as the C-chain indexer does.
The environment variables `NODE_URL` and `NODE_API_KEY` override `url` and `api_key` (`url_variable` and `api_key_variable` rename them); they are the C-chain indexer's, so both services can share them.
Here "node" is the C-chain node serving JSON-RPC, not the TEE node.
Keep the URL and key in the environment rather than the config file; the proxy keeps both out of its logs and errors.

On a fresh start the proxy sends the TEE node its initial signing policy and the voters' public keys, which become the node's root of trust.
The node cannot verify them, and the restart backfill feeds instruction voting the same way, so in `rpc` mode they are only as trustworthy as the endpoint.
Use public endpoints for tests; in production use the indexer or a node you run.

The modes differ in a few places:

- Readiness and `teeproxy_cchain_indexer_delay_seconds` track the node's newest block instead of the last indexed one.
- A machine path list carries every governance signature the contract stored, including ones added after activation.
- A Safe approval comes from the contract's confirmed-approval record (`confirmMachinePathListSafeApproval`) instead of the Safe's own transaction.
- How far back `voting.history_size` reaches, on a fresh start or a restart, depends on the node's log retention instead of the indexer's.

At the 30-block cap a policy rollover takes about 115 calls on mainnet and 45 on Coston2: mostly the voter-registration scan, plus one batch of up to `max_batch_size` (50) transaction lookups.
A fresh start takes about 290 calls on mainnet at the default `voting.history_size` of 3, and about 110 more per extra policy; a restart takes 3 per policy.
Otherwise the proxy makes about 6 calls a minute, plus one per readiness probe and metrics scrape.
Lower `max_batch_size` for an endpoint that limits batches or bursts below 50 calls.

## Ports

The proxy listens on two TCP ports with different trust models:

- **`6662` (external)** — client-facing.
  Public by design: all `GET` routes (`/info`, `/wallet/*`, `/backup/*`, `/action/*`) are unauthenticated.
  `POST /instruction` verifies a per-payload signature; `POST /direct` (if enabled) requires an API key.
  TLS must be terminated upstream (ingress, sidecar, or front-proxy) — the server speaks cleartext HTTP.
- **`6661` (internal)** — TEE-node-facing.
  No app-layer authentication; access control is assumed to be enforced by the network.
  `POST /queue/*` is unauthenticated; `POST /result` verifies the TEE's signature but still relies on network isolation for the startup window.
  This port must **not** be reachable from outside the pod/host.
  Deployments must enforce this (e.g., Kubernetes `NetworkPolicy`, bind to loopback, or sidecar-only access).

## Metrics

Prometheus metrics are exposed on `GET /metrics` of the internal server (port `6661` by default; it follows `ports.internal`).
They are opt-in: nothing is collected and the endpoint is not mounted unless `[metrics] enable = true`.
The endpoint inherits the internal port's trust model — no app-layer authentication, and it must not be reachable from outside the pod/host.

Collection is split into groups that can be toggled independently.
When `enable = true`, an unset group is on; set a group to `false` to omit it.
An omitted group does not collect its data unless that data is already needed elsewhere.
The proxy refuses to start if metrics are enabled with every group disabled.

```toml
[metrics]
enable = true
# storage = false   # omit a single group; unset groups stay on
```

Groups: `http`, `storage`, `queue`, `voting`, `active_voters`, `result`, `wallet`, `info`, `attestation`, `policy`, `liveness`, `node`, `runtime`.
Metric names are prefixed `teeproxy_`; the `runtime` group also exports the standard `go_*` and `process_*` collectors.

## Direct Endpoint

The `POST /direct` endpoint allows submitting direct instructions that bypass the C-chain.
It is disabled by default and must be explicitly enabled in the config:

```toml
direct_extension = true
```

When enabled, the endpoint requires API key authentication via the `X-API-Key` HTTP header.
The API key can be configured in two ways:

1. **Environment variable** (recommended): set `DIRECT_API_KEY` (or a custom variable name via `direct_api_key_variable` in config)
2. **Config file**: set `direct_api_key` in `config.toml`

If both are set, the environment variable takes precedence.
The proxy will refuse to start if `enable_direct` is enabled without a configured API key.

To disable API key protection entirely, set `direct_no_api_key = true` in the config.
When set, the `/direct` endpoint accepts requests without the `X-API-Key` header.

The /direct endpoints expects Direct Instruction as a body

```json
{
  "opType": "0x...",
  "opCommand": "0x...",
  "message": "0x..."
}
```

Example request:

```bash
curl -X POST http://localhost:6662/direct \
  -H "Content-Type: application/json" \
  -H "X-API-Key: {YOUR_API_KEY}" \
  -d '{ ... }'
```

## Docker

### Building

Clone tee-node and tee-proxy repositories and run the following command

```bash
docker build -t {IMAGE_TAG} -f tee-proxy/Dockerfile
```

### Running

The image ships without a config file.
Bind-mount your `config.toml` to `/app/config/config.toml`; without it the proxy exits at startup.

```bash
docker run -p 6661:6661 -p 6662:6662 \
  -e PRIVATE_KEY={PRIVATE_KEY} \
  -v {PATH_TO_CONFIG}:/app/config/config.toml \
  {IMAGE_TAG}
```

With `[chain] source = "rpc"`, pass the C-chain endpoint (not the TEE node) as `-e NODE_URL=...`, and its key as `-e NODE_API_KEY=...` if it needs one.

If you have `indexer-db` and `redis` running in docker-compose add the `--network` flag

```bash
docker run -p 6661:6661 -p 6662:6662 \
  -e PRIVATE_KEY={PRIVATE_KEY} \
  -v {PATH_TO_CONFIG}:/app/config/config.toml \
  --network {NETWORK_NAME} \
  {IMAGE_TAG}
```
