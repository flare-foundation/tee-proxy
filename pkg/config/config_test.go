package config

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestRead(t *testing.T) {
	const path = "./test_configs/config.toml"

	cfg, err := Read(path)
	require.NoError(t, err)

	fmt.Printf("cfg: %v\n", cfg)
}

func TestReadFail(t *testing.T) {
	const path = "./test_configs/config_fail.toml"

	_, err := Read(path)
	require.Error(t, err)

	const nopath = "./test_configs/no.toml"

	_, err = Read(nopath)
	require.Error(t, err)

	fmt.Printf("err: %v\n", err)
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

	os.Setenv("PRIVATE_KEY", keyS)

	readKey, err := PrivateKeyFromEnv("")
	require.NoError(t, err)

	require.True(t, readKey.Equal(key))

	_, err = PrivateKeyFromEnv("NO_PRIVATE_KEY")
	require.Error(t, err)

	os.Setenv("PRIVATE_KEY", "FAIL")
	_, err = PrivateKeyFromEnv("FAIL_PRIVATE_KEY")
	require.Error(t, err)

	os.Clearenv()
}

func TestValidateTiming(t *testing.T) {
	var timing = Timing{
		T0:        1,
		VoteEpoch: 90,
	}

	require.NoError(t, timing.validate())

	timing.VoteEpoch = 0

	require.Error(t, timing.validate())
}

func TestTimestampToVotingEpoch(t *testing.T) {
	// flare
	var tm = Timing{
		T0:        1658430000,
		VoteEpoch: 90,
	}

	ts := uint64(0x6890b28f) // from block 0xa52a662873fdc34ffcb415733c601ff1f24f9885c7579df84737f102d223406d on flare

	expectedVR := uint32(1065370)

	vr := tm.TimestampToVotingEpochID(ts)

	require.Equal(t, expectedVR, vr)
}

func TestValidateStorageTiming(t *testing.T) {
	var timing = StorageTiming{
		CycleInternal:          100 * time.Second,
		CycleQueueResponseWait: 4 * time.Second,
	}

	require.NoError(t, timing.validate())

	timing.CycleInternal = -1 * time.Second
	require.Error(t, timing.validate())

	timing.CycleInternal = time.Second

	timing.CycleQueueResponseWait = 0
	require.Error(t, timing.validate())
}
