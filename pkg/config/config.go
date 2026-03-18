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
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
)

const (
	DefaultPrivateKeyVariable   = "PRIVATE_KEY"
	DefaultDirectAPIKeyVariable = "DIRECT_API_KEY"

	defaultInitialInfoTimeout         = 5 * time.Minute
	defaultCycleInternal              = 10 * time.Second
	defaultCycleQueueResponseWait     = 30 * time.Second
	defaultSigningPolicyFetchInterval = 10 * time.Minute
	defaultInitialSigningPolicyOffset = 3

	defaultProposalExpiration  = 120 * time.Second
	defaultMaxPendingRequests  = uint(100)
	defaultVotingHistorySize   = 3
	defaultFinalizedBufferSize = 10
)

var (
	errSigningPolicyFetchIntervalPositive = errors.New("SigningPolicyFetchInterval has to be positive")
	errInitialSigningPolicyOffsetNegative = errors.New("InitialSigningPolicyOffset cannot be negative")
	errFlareSystemsManagerAddressNotSet   = errors.New("flareSystemsManager address not set")
	errRelayAddressNotSet                 = errors.New("relay address not set")
	errVoterRegistryAddressNotSet         = errors.New("voterRegistry address not set")
	errInternalPortNotSet                 = errors.New("internal port not set")
	errExternalPortNotSet                 = errors.New("external port not set")
	errProposalExpirationPositive         = errors.New("proposalExpiration has to be positive")
	errMaxPendingRequestsPositive         = errors.New("maxPendingRequests has to be positive")
	errHistorySizeTooSmall                = errors.New("historySize has to be at least 2")
	errFinalizedBufferSizePositive        = errors.New("finalizedBufferSize has to be positive")
	errInvalidPrivateKeyString            = errors.New("invalid string for private key")
	errDirectAPIKeyNotSet                 = errors.New("direct_extension is enabled but no API key is configured (set direct_api_key in config or DIRECT_API_KEY env variable)")
)

type Proxy struct {
	DB                         database.Config `toml:"db"`                            // C-chain indexer database config.
	RedisPort                  string          `toml:"redis_port"`                    // Redis database port.
	ChainID                    uint64          `toml:"chain_id"`                      // Chain ID used for voter registration message hash verification.
	Addresses                  Addresses       `toml:"addresses"`                     // Smart contract addresses.
	Ports                      Ports           `toml:"ports"`                         // Servers ports.
	InfoTiming                 InfoTiming      `toml:"info_timing"`                   // Timing configuration for TEE info updates (duration between periodic checks and response timeout)
	Voting                     Voting          `toml:"voting"`                        // Instruction voting configurations.
	PrivateKeyVariable         string          `toml:"private_key_variable"`          // Name of environment variable that stores proxy's private key. Defaults to PRIVATE_KEY.
	InitialSigningPolicyOffset int             `toml:"initial_signing_policy_offset"` // 0 for current signing policy, n for "current - n". If not set it defaults to 3.
	SigningPolicyFetchInterval time.Duration   `toml:"signing_policy_fetch_interval"` // Duration between periodic checks for a new signing policy.
	Logging                    logger.Config   `toml:"logging"`                       // Logging configurations. Default is "DEBUG" level in consol.
	EnableDirect               bool            `toml:"enable_direct"`                 // With EnableDirect set to true, external server has an endpoint to post direct instructions.
	DirectAPIKey               string          `toml:"direct_api_key"`                // API key for the /direct endpoint. Can also be set via env variable (see DirectAPIKeyVariable).
	DirectAPIKeyVariable       string          `toml:"direct_api_key_variable"`       // Name of environment variable that stores the /direct endpoint API key. Defaults to DIRECT_API_KEY.
}

// Read reads Proxy configurations from toml file at path and validates them.
func Read(path string) (Proxy, error) {
	c := Proxy{
		InfoTiming: InfoTiming{
			Initial:                defaultInitialInfoTimeout,
			CycleInternal:          defaultCycleInternal,
			CycleQueueResponseWait: defaultCycleQueueResponseWait,
		},

		Voting: Voting{
			ProposalExpiration:  defaultProposalExpiration,
			MaxPendingRequests:  defaultMaxPendingRequests,
			HistorySize:         defaultVotingHistorySize,
			FinalizedBufferSize: defaultFinalizedBufferSize,
		},

		InitialSigningPolicyOffset: defaultInitialSigningPolicyOffset,
		SigningPolicyFetchInterval: defaultSigningPolicyFetchInterval,

		Logging: logger.Config{
			Level:       "DEBUG",
			File:        "",
			MaxFileSize: 0,
			Console:     true,
		},

		EnableDirect: false,
	}

	err := toml.ReadTo(path, &c, false)
	if err != nil {
		return c, err
	}

	err = c.Voting.validate()
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
		return c, errSigningPolicyFetchIntervalPositive
	}

	if c.InitialSigningPolicyOffset < 0 {
		return c, errInitialSigningPolicyOffsetNegative
	}

	if c.EnableDirect {
		c.DirectAPIKey = resolveDirectAPIKey(c.DirectAPIKeyVariable, c.DirectAPIKey)
		if c.DirectAPIKey == "" {
			return c, errDirectAPIKeyNotSet
		}
	}

	return c, err
}

// resolveDirectAPIKey returns the API key from the environment variable if set,
// otherwise falls back to the TOML config value.
func resolveDirectAPIKey(variableName, fallback string) string {
	if variableName == "" {
		variableName = DefaultDirectAPIKeyVariable
	}

	if v, ok := os.LookupEnv(variableName); ok && v != "" {
		return v
	}

	return fallback
}

// Addresses of the smart contracts.
type Addresses struct {
	FlareSystemsManager common.Address `toml:"flare_systems_manager"`
	Relay               common.Address `toml:"relay"`
	VoterRegistry       common.Address `toml:"voter_registry"`
}

// validate checks that all addresses have nonzero value.
func (a Addresses) validate() error {
	zero := common.Address{}

	if a.FlareSystemsManager.Cmp(zero) == 0 {
		return errFlareSystemsManagerAddressNotSet
	}

	if a.Relay.Cmp(zero) == 0 {
		return errRelayAddressNotSet
	}

	if a.VoterRegistry.Cmp(zero) == 0 {
		return errVoterRegistryAddressNotSet
	}

	return nil
}

type Ports struct {
	Internal string `toml:"internal"`
	External string `toml:"external"`
}

func (a Ports) validate() error {
	if a.Internal == "" {
		return errInternalPortNotSet
	}
	if a.External == "" {
		return errExternalPortNotSet
	}
	return nil
}

type Voting struct {
	ProposalExpiration  time.Duration `toml:"proposal_expiration"`   // Duration the voting for a proposal is open for. Default is 120s
	MaxPendingRequests  uint          `toml:"max_pending_request"`   // Maximal number of open (unfinalized) proposals per provider. It defaults to 100.
	HistorySize         int           `toml:"history_size"`          // Number of most recent signing policy rounds to keep in memory. Defaults to 3.
	FinalizedBufferSize int           `toml:"finalized_buffer_size"` // Buffer size for the finalized instructions channel. Defaults to 10.
}

// SetDefault sets default values if applicable.
func (v *Voting) SetDefault() *Voting {
	if v == nil {
		v = new(Voting)
	}

	if v.MaxPendingRequests == 0 {
		v.MaxPendingRequests = defaultMaxPendingRequests
	}
	if v.ProposalExpiration <= 0 {
		v.ProposalExpiration = defaultProposalExpiration
	}
	if v.HistorySize <= 1 {
		v.HistorySize = defaultVotingHistorySize
	}
	if v.FinalizedBufferSize <= 0 {
		v.FinalizedBufferSize = defaultFinalizedBufferSize
	}

	return v
}

// Validate checks that Voting holds viable values.
func (v *Voting) validate() error {
	if v.ProposalExpiration <= 0 {
		return errProposalExpirationPositive
	}

	if v.MaxPendingRequests <= 0 {
		return errMaxPendingRequestsPositive
	}

	if v.HistorySize <= 1 {
		return errHistorySizeTooSmall
	}

	if v.FinalizedBufferSize <= 0 {
		return errFinalizedBufferSizePositive
	}

	return nil
}

// PrivateKeyFromEnv extracts ecdsa private key from env variable.
//
// Private key should be 32 bytes long hex string, but it can be shorter. It can be 0x or 0X prefixed or not.
func PrivateKeyFromEnv(variableName string) (*ecdsa.PrivateKey, error) {
	if len(variableName) == 0 {
		variableName = DefaultPrivateKeyVariable
	}
	skStr, exists := os.LookupEnv(variableName)

	if !exists {
		return nil, fmt.Errorf("no %s env variable stored", variableName)
	}

	skStr, _ = strings.CutPrefix(skStr, "0x")
	skStr, _ = strings.CutPrefix(skStr, "0X")

	if len(skStr)%2 != 0 {
		skStr = "0" + skStr
	}

	skB, err := hex.DecodeString(skStr)
	if err != nil {
		return nil, errInvalidPrivateKeyString
	}

	return crypto.ToECDSA(skB)
}
