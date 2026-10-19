# Changelog

## [Unreleased]

### Fixed

- Machine path lists that involve several governances are no longer rejected by the TEE node when `[governance]` is configured: only the node's governance signatures are forwarded.
  Direct-signature deployments now need `signers` + `threshold` set to get this; `[governance]` was previously documented as Safe-only.
