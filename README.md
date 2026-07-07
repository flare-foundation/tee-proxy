<div align="center">
  <a href="https://flare.network/" target="blank">
    <img src="https://content.flare.network/Flare-2.svg" width="300" alt="Flare Logo" />
  </a>
  <br />
  <a href="CONTRIBUTING.md">Contributing</a>
  ·
  <a href="SECURITY.md">Security</a>
  ·
  <a href="CHANGELOG.md">Changelog</a>
</div>

# Flare TEE proxy

## Running

Copy config.example.toml to config.toml

```bash
cp ./config.example.toml ./config/config.toml
```

and set the configurations.

Make sure that the proxy's private key is stored in the environment variable `PRIVATE_KEY`.
If you want it read from a different environment, set specify the name in config under `private_key_variable`

Start the proxy

```bash
go run ./...
```

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

Groups: `http`, `storage`, `queue`, `voting`, `active_voters`, `result`, `info`, `attestation`, `policy`, `liveness`, `runtime`.
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

```bash
docker run -p 6661:6661 -p 6662:6662 \
  -e PRIVATE_KEY={PRIVATE_KEY} \
  -v {PATH_TO_CONFIG}:/app/config/config.toml \
  {IMAGE_TAG}
```

If you have `indexer-db` and `redis` running in docker-compose add the `--network` flag

```bash
docker run -p 6661:6661 -p 6662:6662 \
  -e PRIVATE_KEY={PRIVATE_KEY} \
  -v {PATH_TO_CONFIG}:/app/config/config.toml \
  --network {NETWORK_NAME} \
  {IMAGE_TAG}
```
