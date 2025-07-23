<p align="left">
  <a href="https://flare.network/" target="blank"><img src="https://content.flare.network/Flare-2.svg" width="500" height="100" alt="Flare Logo" /></a>
</p>

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
