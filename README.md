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
