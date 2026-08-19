package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/toml"
	"github.com/stretchr/testify/require"
)

func TestRead(t *testing.T) {
	const path = "./test_configs/config.toml"

	_, err := Read(path)
	require.NoError(t, err)
}

func TestReadDirectAPIKeyOptional(t *testing.T) {
	const path = "./test_configs/config_direct_api_key_optional.toml"

	cfg, err := Read(path)
	require.NoError(t, err)
	require.True(t, cfg.Direct.Enable)
	require.True(t, cfg.Direct.APIKeyOptional)
	require.Empty(t, cfg.Direct.APIKey)
}

func TestReadFail(t *testing.T) {
	const path = "./test_configs/config_fail.toml"

	_, err := Read(path)
	require.Error(t, err)

	const nopath = "./test_configs/no.toml"

	_, err = Read(nopath)
	require.Error(t, err)
}

func TestValidateAddresses(t *testing.T) {
	nonzeroAdr := common.HexToAddress("aaaa")

	a := Addresses{
		FlareSystemsManager: nonzeroAdr,
		Relay:               nonzeroAdr,
		VoterRegistry:       nonzeroAdr,
	}

	require.NoError(t, a.validate())

	a.FlareSystemsManager = common.Address{}
	require.Error(t, a.validate())

	a.FlareSystemsManager = nonzeroAdr
	a.Relay = common.Address{}
	require.Error(t, a.validate())

	a.Relay = nonzeroAdr
	a.VoterRegistry = common.Address{}
	require.Error(t, a.validate())
}

func TestPrivateKeyFromEnv(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	keyS := key.D.Text(16)

	t.Setenv("PRIVATE_KEY", keyS)

	readKey, err := PrivateKeyFromEnv("")
	require.NoError(t, err)

	require.True(t, readKey.Equal(key))

	_, err = PrivateKeyFromEnv("NO_PRIVATE_KEY")
	require.Error(t, err)

	t.Setenv("PRIVATE_KEY", "FAIL")
	_, err = PrivateKeyFromEnv("FAIL_PRIVATE_KEY")
	require.Error(t, err)

	os.Clearenv()
}

func TestValidateStorageTiming(t *testing.T) {
	var timing = InfoTiming{
		CycleInternal:          100 * time.Second,
		CycleQueueResponseWait: 4 * time.Second,
		MaxAttempts:            3,
		RetryDelay:             time.Second,
	}

	require.NoError(t, timing.validate())

	timing.CycleInternal = -1 * time.Second
	require.Error(t, timing.validate())

	timing.CycleInternal = time.Second

	timing.CycleQueueResponseWait = 0
	require.Error(t, timing.validate())

	timing.CycleQueueResponseWait = time.Second
	timing.Initial = -1 * time.Second
	require.Error(t, timing.validate())

	timing.Initial = 0 // "no timeout" sentinel is accepted
	require.NoError(t, timing.validate())

	timing.MaxAttempts = 0
	require.Error(t, timing.validate())

	timing.MaxAttempts = 1
	timing.RetryDelay = 0 // a zero delay would let the bootstrap retry loop hammer the node
	require.Error(t, timing.validate())
}

func TestConfig(t *testing.T) {
	tests := []struct {
		before Voting
		after  Voting
	}{
		{
			before: Voting{},
			after: Voting{
				ProposalExpiration:  defaultProposalExpiration,
				MaxPendingRequests:  defaultMaxPendingRequests,
				HistorySize:         defaultVotingHistorySize,
				FinalizedBufferSize: defaultFinalizedBufferSize,
			},
		},
		{
			before: Voting{
				ProposalExpiration: 1,
				MaxPendingRequests: 1,
			},
			after: Voting{
				ProposalExpiration:  1,
				MaxPendingRequests:  1,
				HistorySize:         defaultVotingHistorySize,
				FinalizedBufferSize: defaultFinalizedBufferSize,
			},
		},
		{
			before: Voting{
				ProposalExpiration:  -10,
				MaxPendingRequests:  1,
				HistorySize:         1,
				FinalizedBufferSize: 1,
			},
			after: Voting{
				ProposalExpiration:  defaultProposalExpiration,
				MaxPendingRequests:  1,
				HistorySize:         defaultVotingHistorySize,
				FinalizedBufferSize: 1,
			},
		},
		{
			before: Voting{
				ProposalExpiration:  10,
				MaxPendingRequests:  0,
				HistorySize:         3,
				FinalizedBufferSize: 1,
			},
			after: Voting{
				ProposalExpiration:  10,
				MaxPendingRequests:  defaultMaxPendingRequests,
				HistorySize:         3,
				FinalizedBufferSize: 1,
			},
		},
	}

	for _, test := range tests {
		test.before.SetDefault()
		require.Equal(t, test.before, test.after)
	}
}

func TestValidateGCS(t *testing.T) {
	tests := []struct {
		name string
		g    GCS
		err  error
	}{
		{"empty means redis-backed", GCS{}, nil},
		{"bucket only", GCS{Bucket: "b"}, nil},
		{"bucket with emulator url", GCS{Bucket: "b", URL: "http://localhost:4443/storage/v1/"}, nil},
		{"bucket with credentials", GCS{Bucket: "b", CredentialsFile: "sa.json"}, nil},
		{"configured without bucket", GCS{Prefix: "p"}, errGCSBucketNotSet},
		{"url and credentials together", GCS{Bucket: "b", URL: "http://localhost:4443", CredentialsFile: "sa.json"}, errGCSURLWithCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.g.validate()
			if tt.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.err)
			}
		})
	}
}

func TestGovernanceValidate(t *testing.T) {
	s := []common.Address{
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		common.HexToAddress("0x2222222222222222222222222222222222222222"),
	}
	safe := common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")
	mgr := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")

	tests := []struct {
		name    string
		gov     Governance
		wantErr bool
	}{
		{"unset is valid (legacy path)", Governance{}, false},
		{"plain valid", Governance{Signers: s, Threshold: 2}, false},
		{"safe valid", Governance{Signers: s, Threshold: 2, Safe: safe, TeeManager: mgr}, false},
		{"signers without threshold", Governance{Signers: s}, true},
		{"threshold without signers", Governance{Threshold: 1}, true},
		{"threshold exceeds signers", Governance{Signers: s, Threshold: 3}, true},
		{"zero signer", Governance{Signers: []common.Address{{}}, Threshold: 1}, true},
		{"safe without tee manager", Governance{Signers: s, Threshold: 2, Safe: safe}, true},
		{"tee manager without safe", Governance{Signers: s, Threshold: 2, TeeManager: mgr}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.gov.validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestReadGovernanceWithoutManager guards the cross-field check: [governance]
// configures machine-path pre-verification only, so setting it without
// addresses.machine_path_manager would be silently inert — Read refuses it.
func TestReadGovernanceWithoutManager(t *testing.T) {
	base, err := os.ReadFile("./test_configs/config.toml")
	require.NoError(t, err)

	gov := "\n[governance]\nsigners = [\"0x1111111111111111111111111111111111111111\", \"0x2222222222222222222222222222222222222222\"]\nthreshold = 2\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, append(base, gov...), 0o600))

	_, err = Read(path)
	require.ErrorIs(t, err, errGovernanceWithoutManager)
}

func TestMetricsValidate(t *testing.T) {
	off := func() *bool { b := false; return &b }
	on := func() *bool { b := true; return &b }

	allOff := Metrics{
		HTTP: off(), Storage: off(), Queue: off(), Voting: off(),
		ActiveVoters: off(), Result: off(), Wallet: off(), Info: off(), Attestation: off(),
		Policy: off(), Liveness: off(), Node: off(), Runtime: off(),
	}

	tests := []struct {
		name    string
		metrics Metrics
		wantErr bool
	}{
		{"disabled is valid", Metrics{Enable: false}, false},
		{"enabled, groups unset (inherit on)", Metrics{Enable: true}, false},
		{"enabled, one group explicitly on", Metrics{Enable: true, HTTP: off(), Runtime: on()}, false},
		{"enabled, all groups off", func() Metrics { m := allOff; m.Enable = true; return m }(), true},
		{"disabled, all groups off", func() Metrics { m := allOff; m.Enable = false; return m }(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.metrics.validate()
			if tt.wantErr {
				require.ErrorIs(t, err, errMetricsNoGroups)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestMetricsTomlDecode pins the [metrics] *bool struct tags to their TOML keys.
// It round-trips a real TOML snippet through the same toml.ReadTo wrapper Read uses,
// so a renamed or typo'd tag (or a tag swap between groups) makes the assertions fail.
// The "absent" case is exercised by omitting the key; allowUnknownFields is false, so
// adding an unknown key would error instead.
func TestMetricsTomlDecode(t *testing.T) {
	const snippet = `[metrics]
enable = true

http = true
storage = false
runtime = true
`

	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.toml")
	require.NoError(t, os.WriteFile(path, []byte(snippet), 0o600))

	var p Proxy
	require.NoError(t, toml.ReadTo(path, &p, false))

	m := p.Metrics
	require.True(t, m.Enable)

	tests := []struct {
		name string
		got  *bool
		want *bool // nil means the key was absent
	}{
		{"http present true", m.HTTP, ptr(true)},
		{"storage present false", m.Storage, ptr(false)},
		{"runtime present true", m.Runtime, ptr(true)},
		{"queue absent", m.Queue, nil},
		{"voting absent", m.Voting, nil},
		{"active_voters absent", m.ActiveVoters, nil},
		{"result absent", m.Result, nil},
		{"info absent", m.Info, nil},
		{"attestation absent", m.Attestation, nil},
		{"policy absent", m.Policy, nil},
		{"liveness absent", m.Liveness, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want == nil {
				require.Nil(t, tt.got)
				return
			}
			require.NotNil(t, tt.got)
			require.Equal(t, *tt.want, *tt.got)
		})
	}
}

func ptr(b bool) *bool { return &b }

// TestMetricsValidateCoversEveryGroup asserts validate() inspects every group field, so a
// newly added group cannot silently escape the "enabled but all groups off" guard. A group
// missing from validate()'s slice would let "only that group on, all others off" be wrongly
// rejected — this catches that per group.
func TestMetricsValidateCoversEveryGroup(t *testing.T) {
	boolPtr := reflect.TypeFor[*bool]()
	mt := reflect.TypeFor[Metrics]()

	for i := 0; i < mt.NumField(); i++ {
		if mt.Field(i).Type != boolPtr {
			continue // skip the Enable bool and any non-group field
		}
		name := mt.Field(i).Name
		t.Run(name, func(t *testing.T) {
			// Master on; this group on; every other group explicitly off.
			m := Metrics{Enable: true}
			v := reflect.ValueOf(&m).Elem()
			for j := 0; j < mt.NumField(); j++ {
				if mt.Field(j).Type != boolPtr {
					continue
				}
				v.Field(j).Set(reflect.ValueOf(ptr(j == i)))
			}
			require.NoError(t, m.validate(),
				"group %s on with all others off must validate; is it missing from validate()'s slice?", name)
		})
	}
}
