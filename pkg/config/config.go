package config

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
)

const DefaultPrivateKeyVariable = "PRIVATE_KEY"

const (
	defaultCycleInternal              = 10 * time.Second
	defaultCycleQueueResponseWait     = 5 * time.Second
	defaultSigningPolicyFetchInterval = 10 * time.Minute
	defaultInitialSigningPolicyOffset = 3
)

type Proxy struct {
	DB                         database.Config `toml:"db"`                            // C-chain indexer database config.
	RedisPort                  string          `toml:"redis_port"`                    // Redis database port.
	Addresses                  Addresses       `toml:"addresses"`                     // Smart contract addresses.
	Ports                      Ports           `toml:"ports"`                         // Servers ports.
	InfoTiming                 InfoTiming      `toml:"info_timing"`                   // Timing configuration for TEE info updates (duration between periodic checks and response timeout)
	Voting                     voting.Config   `toml:"voting"`                        // Instruction voting configurations.
	PrivateKeyVariable         string          `toml:"private_key_variable"`          // Name of environment variable that stores proxy's private key. Defaults to PRIVATE_KEY.
	InitialSigningPolicyOffset int             `toml:"initial_signing_policy_offset"` // 0 for current signing policy, n for "current - n". If not set it defaults to 3.
	SigningPolicyFetchInterval time.Duration   `toml:"signing_policy_fetch_interval"` // Duration between periodic checks for a new signing policy.
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
	c := Proxy{
		InfoTiming: InfoTiming{
			CycleInternal:          defaultCycleInternal,
			CycleQueueResponseWait: defaultCycleQueueResponseWait,
		},

		InitialSigningPolicyOffset: defaultInitialSigningPolicyOffset,
		SigningPolicyFetchInterval: defaultSigningPolicyFetchInterval,
	}

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

	err = c.InfoTiming.validate()
	if err != nil {
		return c, err
	}

	if c.SigningPolicyFetchInterval <= 0 {
		return c, fmt.Errorf("SigningPolicyFetchInterval has to be positive")
	}

	if c.InitialSigningPolicyOffset < 0 {
		return c, fmt.Errorf("InitialSigningPolicyOffset cannot be negative")
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

// PrivateKeyFromEnv extracts ecdsa private key from env variable.
//
// Private key should be 32 bytes long hex string, but it can be shorter. It can be 0x or 0X prefixed or not.
func PrivateKeyFromEnv(variableName string) (*ecdsa.PrivateKey, error) {
	if len(variableName) == 0 {
		variableName = DefaultPrivateKeyVariable
	}
	skStr := os.Getenv(variableName)

	skStr, _ = strings.CutPrefix(skStr, "0x")
	skStr, _ = strings.CutPrefix(skStr, "0X")

	if len(skStr)%2 != 0 {
		skStr = "0" + skStr
	}

	skB, err := hex.DecodeString(skStr)
	if err != nil {
		return nil, fmt.Errorf("invalid string for private key")
	}

	skB = prefixTo32Bytes(skB)

	return crypto.ToECDSA(skB)
}

func prefixTo32Bytes(s []byte) []byte {
	if len(s) >= 32 {
		return s
	}

	rs := make([]byte, 32-len(s), 32)

	rs = append(rs, s...)

	return rs
}
