package config

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
)

type Proxy struct {
	DB        database.Config
	RedisPort string
	Addresses Addresses
	Ports     Ports
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

func Read(path string) (Proxy, error) {
	c := Proxy{}

	err := toml.ReadTo(path, &c, false)

	return c, err
}
