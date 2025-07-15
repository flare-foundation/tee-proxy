package config

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
)

type Proxy struct {
	DB database.Config
}

type Addresses struct {
	FlareSystemsManager common.Address `toml:"flare_systems_manager"`
	Relay               common.Address `toml:"relay"`
	VoterRegistry       common.Address `toml:"voter_registry"`
}
