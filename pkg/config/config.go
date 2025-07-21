package config

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
)

type Proxy struct {
	DB        database.Config
	RedisPort string
	Addresses Addresses
	Ports     Ports
	Timing    Timing
	PrivKey   []byte // todo make this safe
}

type Addresses struct {
	FlareSystemsManager common.Address `toml:"flare_systems_manager"`
	Relay               common.Address `toml:"relay"`
	VoterRegistry       common.Address `toml:"voter_registry"`
}

type Ports struct {
	Internal string
	External string
}

// Read reads Proxy configurations from toml file at path.
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
