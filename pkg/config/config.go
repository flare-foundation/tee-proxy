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

	defaultMachinePathListFetchInterval = 10 * time.Minute

	defaultProposalExpiration  = 120 * time.Second
	defaultMaxPendingRequests  = uint(100)
	defaultVotingHistorySize   = 3
	defaultFinalizedBufferSize = 10

	defaultDBSyncMaxSleepTime = 10 * time.Minute

	defaultActionTTL       = 14 * 24 * time.Hour
	defaultResultTTL       = 14 * 24 * time.Hour
	defaultSubmitResultTTL = 30 * time.Minute
	defaultBackupTTL       = 8 * 24 * time.Hour
)

var (
	errSigningPolicyFetchIntervalPositive   = errors.New("signingPolicyFetchInterval has to be positive")
	errMachinePathListFetchIntervalPositive = errors.New("machinePathListFetchInterval has to be positive")
	errInitialSigningPolicyOffsetNegative   = errors.New("initialSigningPolicyOffset cannot be negative")
	errFlareSystemsManagerAddressNotSet     = errors.New("flareSystemsManager address not set")
	errRelayAddressNotSet                   = errors.New("relay address not set")
	errVoterRegistryAddressNotSet           = errors.New("voterRegistry address not set")
	errInternalPortNotSet                   = errors.New("internal port not set")
	errExternalPortNotSet                   = errors.New("external port not set")
	errProposalExpirationPositive           = errors.New("proposalExpiration has to be positive")
	errMaxPendingRequestsPositive           = errors.New("maxPendingRequests has to be positive")
	errHistorySizeTooSmall                  = errors.New("historySize has to be at least 2")
	errFinalizedBufferSizePositive          = errors.New("finalizedBufferSize has to be positive")
	errMaxProviderVoteOutOfRange            = errors.New("maxProviderVote must be in (0, 1] or 0 (unset)")
	errInvalidPrivateKeyString              = errors.New("invalid string for private key")
	errDirectAPIKeyNotSet                   = errors.New("direct_extension is enabled but no API key is configured (set direct_api_key in config or DIRECT_API_KEY env variable)")
	errStorageTTLPositive                   = errors.New("storage ttl values have to be positive")
	errAttestationCodeHashInvalid           = errors.New("attestation expected_code_hashes contains invalid hex")
	errAttestationMaxTokenAgeNegative       = errors.New("attestation max_token_age must be non-negative")
	errDirectMaxBodySizeNegative            = errors.New("direct max_body_size must be non-negative")
	errMetricsNoGroups                      = errors.New("metrics enabled but all groups are disabled; enable at least one group or set enable = false")
)

// Firestore holds Firestore connection configuration.
type Firestore struct {
	URL             string `toml:"url"`              // Firestore URL
	ProjectID       string `toml:"project_id"`       // Google Cloud project ID.
	DatabaseID      string `toml:"database_id"`      // Firestore database ID. Empty means default database.
	CredentialsFile string `toml:"credentials_file"` // Path to service account JSON key. Empty means Application Default Credentials.
}

// Proxy holds the full configuration for the TEE proxy service.
type Proxy struct {
	DB                           database.Config `toml:"db"`                               // C-chain indexer database config.
	RedisPort                    string          `toml:"redis_port"`                       // Redis database port.
	Firestore                    Firestore       `toml:"firestore"`                        // Firestore connection configuration.
	ChainID                      uint64          `toml:"chain_id"`                         // EVM chain ID bound into TEE/FDC2 signed payloads. Must be a positive integer.
	Addresses                    Addresses       `toml:"addresses"`                        // Smart contract addresses.
	Ports                        Ports           `toml:"ports"`                            // Servers ports.
	InfoTiming                   InfoTiming      `toml:"info_timing"`                      // Timing configuration for TEE info updates (duration between periodic checks and response timeout)
	Voting                       Voting          `toml:"voting"`                           // Instruction voting configurations.
	PrivateKeyVariable           string          `toml:"private_key_variable"`             // Name of environment variable that stores proxy's private key. Defaults to PRIVATE_KEY.
	InitialSigningPolicyOffset   int             `toml:"initial_signing_policy_offset"`    // 0 for current signing policy, n for "current - n". If not set it defaults to 3.
	SigningPolicyFetchInterval   time.Duration   `toml:"signing_policy_fetch_interval"`    // Duration between periodic checks for a new signing policy.
	MachinePathListFetchInterval time.Duration   `toml:"machine_path_list_fetch_interval"` // Duration between periodic checks for a newly signed machine path list. Defaults to 10m.
	DBSyncMaxSleepTime           time.Duration   `toml:"db_sync_max_sleep_time"`           // Max sleep between DB sync retries on startup. Defaults to 10m.
	Logging                      logger.Config   `toml:"logging"`                          // Logging configurations. Default is "DEBUG" level in consol.
	Direct                       Direct          `toml:"direct"`                           // Direct endpoint configuration.
	Storage                      Storage         `toml:"storage"`                          // TTLs applied to Redis/Firestore-backed persistent storage.
	Attestation                  Attestation     `toml:"attestation"`                      // Bootstrap attestation verification configuration.
	Metrics                      Metrics         `toml:"metrics"`                          // Prometheus metrics. Off by default; opt-in via [metrics] enable.
}

// Attestation controls bootstrap attestation verification.
// Empty allowlists / zero values skip the corresponding check.
type Attestation struct {
	Enable bool `toml:"enable"`

	Audience              string   `toml:"audience"`                // expected aud claim; empty skips the audience check
	ExpectedCodeHashes    []string `toml:"expected_code_hashes"`    // 32-byte hex; accepts an optional 0x or sha256: prefix, but not both
	ExpectedPlatforms     []string `toml:"expected_platforms"`      // hwmodel strings, e.g. "AMD_SEV_SNP_VM"
	ExpectedDebugStatuses []string `toml:"expected_debug_statuses"` // dbgstat values, e.g. "disabled-since-boot"

	MaxTokenAge    time.Duration `toml:"max_token_age"`
	RequireSecBoot bool          `toml:"require_sec_boot"`

	// AllowMagicPass accepts the tee-node sentinel; dev/test only.
	AllowMagicPass bool `toml:"allow_magic_pass"`
}

func (a Attestation) validate() error {
	if a.MaxTokenAge < 0 {
		return errAttestationMaxTokenAgeNegative
	}
	for _, h := range a.ExpectedCodeHashes {
		if _, err := parseCodeHash(h); err != nil {
			return fmt.Errorf("%w: %q: %v", errAttestationCodeHashInvalid, h, err)
		}
	}
	return nil
}

// ParsedCodeHashes decodes ExpectedCodeHashes; safe after validate() passes.
func (a Attestation) ParsedCodeHashes() ([]common.Hash, error) {
	out := make([]common.Hash, 0, len(a.ExpectedCodeHashes))
	for _, h := range a.ExpectedCodeHashes {
		parsed, err := parseCodeHash(h)
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", h, err)
		}
		out = append(out, parsed)
	}
	return out, nil
}

func parseCodeHash(s string) (common.Hash, error) {
	switch {
	case strings.HasPrefix(s, "sha256:"):
		s = s[len("sha256:"):]
	case strings.HasPrefix(s, "0x"), strings.HasPrefix(s, "0X"):
		s = s[2:]
	}

	b, err := hex.DecodeString(s)
	if err != nil {
		return common.Hash{}, err
	}
	if len(b) != common.HashLength {
		return common.Hash{}, fmt.Errorf("expected %d bytes, got %d", common.HashLength, len(b))
	}
	return common.BytesToHash(b), nil
}

// Storage holds TTLs for persistent stores (Redis/Firestore).
type Storage struct {
	ActionTTL       time.Duration `toml:"action_ttl"`        // Retention for queued action bodies. Defaults to 14 days.
	ResultTTL       time.Duration `toml:"result_ttl"`        // Retention for Threshold/End results. Defaults to 14 days.
	SubmitResultTTL time.Duration `toml:"submit_result_ttl"` // Retention for Submit results. Defaults to 30 minutes.
	BackupTTL       time.Duration `toml:"backup_ttl"`        // Retention for backup bodies and index. Defaults to 8 days.
}

func (s *Storage) SetDefault() {
	if s.ActionTTL <= 0 {
		s.ActionTTL = defaultActionTTL
	}
	if s.ResultTTL <= 0 {
		s.ResultTTL = defaultResultTTL
	}
	if s.SubmitResultTTL <= 0 {
		s.SubmitResultTTL = defaultSubmitResultTTL
	}
	if s.BackupTTL <= 0 {
		s.BackupTTL = defaultBackupTTL
	}
}

func (s *Storage) validate() error {
	if s.ActionTTL <= 0 || s.ResultTTL <= 0 || s.SubmitResultTTL <= 0 || s.BackupTTL <= 0 {
		return errStorageTTLPositive
	}
	return nil
}

// Metrics controls the Prometheus metrics subsystem, off by default.
// Each group is a *bool: unset inherits Enable, an explicit false omits just that group.
type Metrics struct {
	Enable bool `toml:"enable"` // Master switch. When false, the whole subsystem is inert.

	HTTP         *bool `toml:"http"`          // Per-request count and latency middleware.
	Storage      *bool `toml:"storage"`       // Generic Redis/Firestore operation count and errors.
	Queue        *bool `toml:"queue"`         // Action enqueue/dequeue counters and queue-depth gauge.
	Voting       *bool `toml:"voting"`        // Instruction and votings-started counters, threshold-duration histogram.
	ActiveVoters *bool `toml:"active_voters"` // Per-epoch participant gauges (data-provider voters, initiators, top providers).
	Result       *bool `toml:"result"`        // Result throughput, lost, and discarded counters.
	Info         *bool `toml:"info"`          // TEE info per-stage refresh failures.
	Attestation  *bool `toml:"attestation"`   // Attestation verify outcomes.
	Policy       *bool `toml:"policy"`        // Active signing-policy reward-epoch gauge.
	Liveness     *bool `toml:"liveness"`      // Readiness gauge and info-staleness gauge.
	Node         *bool `toml:"node"`          // TEE-node response-wait latency and outcomes (info, wallet, machinepath).
	Runtime      *bool `toml:"runtime"`       // Go runtime/process collectors and build info.
}

// validate rejects an enabled subsystem with every group turned off, which would
// run the endpoint while collecting nothing.
func (m Metrics) validate() error {
	if !m.Enable {
		return nil
	}

	// A nil group inherits Enable (on); only an explicit false omits it.
	for _, g := range []*bool{m.HTTP, m.Storage, m.Queue, m.Voting, m.ActiveVoters, m.Result, m.Info, m.Attestation, m.Policy, m.Liveness, m.Node, m.Runtime} {
		if g == nil || *g {
			return nil
		}
	}

	return errMetricsNoGroups
}

// Direct holds configuration for the /direct endpoint.
type Direct struct {
	Enable         bool   `toml:"enable"`           // Enable registers the /direct endpoint on the external server.
	APIKey         string `toml:"api_key"`          // APIKey for the /direct endpoint. Can also be set via env variable (see APIKeyVariable).
	APIKeyVariable string `toml:"api_key_variable"` // APIKeyVariable is the name of environment variable that stores the /direct endpoint API key. Defaults to DIRECT_API_KEY.
	APIKeyOptional bool   `toml:"api_key_optional"` // APIKeyOptional disables the API key requirement for the /direct endpoint.
	MaxBodySize    int64  `toml:"max_body_size"`    // MaxBodySize limits the body of the request on the /direct endpoint. If 0, the server applies its 10 MiB default.
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

		InitialSigningPolicyOffset:   defaultInitialSigningPolicyOffset,
		SigningPolicyFetchInterval:   defaultSigningPolicyFetchInterval,
		MachinePathListFetchInterval: defaultMachinePathListFetchInterval,
		DBSyncMaxSleepTime:           defaultDBSyncMaxSleepTime,

		Storage: Storage{
			ActionTTL:       defaultActionTTL,
			ResultTTL:       defaultResultTTL,
			SubmitResultTTL: defaultSubmitResultTTL,
			BackupTTL:       defaultBackupTTL,
		},

		Logging: logger.Config{
			Level:       "DEBUG",
			File:        "",
			MaxFileSize: 0,
			Console:     true,
		},

		Direct: Direct{},
	}

	err := toml.ReadTo(path, &c, false)
	if err != nil {
		return c, err
	}

	if c.ChainID == 0 {
		return c, errors.New("chain ID must be a positive integer")
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

	if c.MachinePathListFetchInterval <= 0 {
		return c, errMachinePathListFetchIntervalPositive
	}

	err = c.Storage.validate()
	if err != nil {
		return c, err
	}

	if c.Direct.MaxBodySize < 0 {
		return c, errDirectMaxBodySizeNegative
	}

	if c.Direct.Enable && !c.Direct.APIKeyOptional {
		c.Direct.APIKey = resolveDirectAPIKey(c.Direct.APIKeyVariable, c.Direct.APIKey)
		if c.Direct.APIKey == "" {
			return c, errDirectAPIKeyNotSet
		}
	}

	err = c.Attestation.validate()
	if err != nil {
		return c, err
	}

	err = c.Metrics.validate()
	if err != nil {
		return c, err
	}

	return c, nil
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
	MachinePathManager  common.Address `toml:"machine_path_manager"` // optional: when unset, the machine path list service is disabled.
}

// validate checks that all required addresses have nonzero value.
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

// Ports holds the listen port configuration for the internal and external HTTP servers.
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

// Voting holds tuning parameters for the instruction voting consensus.
type Voting struct {
	ProposalExpiration  time.Duration `toml:"proposal_expiration"`   // Duration the voting for a proposal is open for. Default is 120s
	MaxPendingRequests  uint          `toml:"max_pending_request"`   // Maximal number of open (unfinalized) proposals per provider. It defaults to 100.
	HistorySize         int           `toml:"history_size"`          // Number of most recent signing policy rounds to keep in memory. Defaults to 3.
	FinalizedBufferSize int           `toml:"finalized_buffer_size"` // Buffer size for the finalized instructions channel. Defaults to 10.
	MaxProviderVote     float64       `toml:"max_provider_vote"`     // Maximal possible fraction (0,1] of total weight a single provider can hold. Used to cap per-vote variable message size for restore operations. If unset, no cap is applied.
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

// validate checks that Voting holds viable values.
func (v *Voting) validate() error {
	if v.ProposalExpiration <= 0 {
		return errProposalExpirationPositive
	}

	if v.MaxPendingRequests == 0 {
		return errMaxPendingRequestsPositive
	}

	if v.HistorySize <= 1 {
		return errHistorySizeTooSmall
	}

	if v.FinalizedBufferSize <= 0 {
		return errFinalizedBufferSizePositive
	}

	if v.MaxProviderVote < 0 || v.MaxProviderVote > 1 {
		return errMaxProviderVoteOutOfRange
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
