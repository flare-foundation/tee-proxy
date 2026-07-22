package config

import (
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
