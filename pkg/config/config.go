package config

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
)

const defaultPrivateKeyVariable = "PRIVATE_KEY"

type Proxy struct {
	DB                 database.Config `toml:"db"`                   // C-chain indexer database config.
	RedisPort          string          `toml:"redis_port"`           // Redis database port.
	Addresses          Addresses       `toml:"addresses"`            // Smart contract addresses.
	Ports              Ports           `toml:"ports"`                // Servers ports.
	Timing             Timing          `toml:"timing"`               // Chain timing.
	StorageTiming      StorageTiming   `toml:"storage_timing"`       // Storage timing
	Voting             voting.Config   `toml:"voting"`               // Instruction voting configurations.
	PrivateKeyVariable string          `toml:"private_key_variable"` // Name of environment variable that stores proxy's private key. Defaults to PRIVATE_KEY.
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

	err = c.StorageTiming.validate()
	if err != nil {
		return c, err
	}

	c.StorageTiming.CycleInternal = c.StorageTiming.CycleInternal * time.Second
	c.StorageTiming.CycleQueueResponseWait = c.StorageTiming.CycleQueueResponseWait * time.Second

	c.Voting.ProposalExpiration = c.Voting.ProposalExpiration * time.Second

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

// PrivateKeyFromEnv extracts ecdsa private key from env variable.
//
// Private key should be 32 bytes long hex string. It can be 0x or 0
func PrivateKeyFromEnv(variableName string) (*ecdsa.PrivateKey, error) {
	if len(variableName) == 0 {
		variableName = defaultPrivateKeyVariable
	}
	pkStr := os.Getenv(variableName)

	if len(pkStr) == 0 {
		return nil, errors.New("private key not set")
	}

	pkStr, _ = strings.CutPrefix(pkStr, "0x")
	pkStr, _ = strings.CutPrefix(pkStr, "0X")

	pkB, err := hex.DecodeString(pkStr)
	if err != nil {
		return nil, errors.New("invalid string for private key")
	}

	return crypto.ToECDSA(pkB)
}
