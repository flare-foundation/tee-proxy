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

## Direct Endpoint

The `/direct` endpoint allows submitting direct instructions that bypass voting consensus.
It is disabled by default and must be explicitly enabled in the config:

```toml
direct_extension = true
```

When enabled, the endpoint requires API key authentication via the `X-API-Key` HTTP header.
The API key can be configured in two ways:

1. **Environment variable** (recommended): set `DIRECT_API_KEY` (or a custom variable name via `direct_api_key_variable` in config)
2. **Config file**: set `direct_api_key` in `config.toml`

If both are set, the environment variable takes precedence.
The proxy will refuse to start if `direct_extension` is enabled without a configured API key.

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
