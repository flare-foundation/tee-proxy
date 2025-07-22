package config

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
)

type Proxy struct {
	DB         database.Config `toml:"db"`         // c-chain indexer database config
	RedisPort  string          `toml:"redis_port"` // redis database port
	Addresses  Addresses       `toml:"addresses"`  // smart contract addresses
	Ports      Ports           `toml:"ports"`
	Timing     Timing          `toml:"timing"`
	PrivateKey []byte          `toml:"private_key"` // todo make this safe
}

type Addresses struct {
	FlareSystemsManager common.Address `toml:"flare_systems_manager"`
	Relay               common.Address `toml:"relay"`
	VoterRegistry       common.Address `toml:"voter_registry"`
}

type Ports struct {
	Internal string `toml:"internal"`
	External string `toml:"external"`
}

// Read reads Proxy configurations from toml file at path and validates them.
func Read(path string) (Proxy, error) {
	c := Proxy{}

	err := toml.ReadTo(path, &c, false)
	if err != nil {
		return c, err
	}

	err = c.Addresses.validate()
	if err != nil {
		return c, err
	}

	err = c.Ports.validate()
	if err != nil {
		return c, err
	}

	err = c.Timing.validate()
	if err != nil {
		return c, err
	}

	return c, err
}

// validate checks that all addresses have nonzero value.
func (a Addresses) validate() error {
	zero := common.Address{}

	if a.FlareSystemsManager.Cmp(zero) == 0 {
		return errors.New("flareSystemsManager address not set")
	}

	if a.Relay.Cmp(zero) == 0 {
		return errors.New("relay address not set")
	}

	if a.VoterRegistry.Cmp(zero) == 0 {
		return errors.New("voterRegistry address not set")
	}

	return nil
}

func (a Ports) validate() error {
	// todo
	return nil
}
