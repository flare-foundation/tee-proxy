package jsonrpc

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/contractregistry"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

const (
	// liveURLVariable names the endpoint TestLive reads, e.g. https://coston2-api.flare.network/ext/C/rpc.
	liveURLVariable = "TEEPROXY_LIVE_RPC_URL"
	// liveRelayVariable optionally overrides the registry's Relay, e.g. with a TEE deployment's.
	liveRelayVariable = "TEEPROXY_LIVE_RELAY"
)

// flareContractRegistry is at the same address on every Flare network.
var flareContractRegistry = common.HexToAddress("0xaD67FE66660Fb8dFE9d6b1b4240d8650e30F6019")

// TestLive builds the latest signing-policy rollover from a real node: the new
// voters' public keys must recover and the collected signatures pass the threshold.
func TestLive(t *testing.T) {
	url := os.Getenv(liveURLVariable)
	if url == "" {
		t.Skip(liveURLVariable + " not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	eth, err := ethclient.DialContext(ctx, url)
	require.NoError(t, err)
	defer eth.Close()

	id, err := eth.ChainID(ctx)
	require.NoError(t, err)
	reg, err := contractregistry.NewContractRegistryCaller(flareContractRegistry, eth)
	require.NoError(t, err)
	address := func(name string) common.Address {
		a, err := reg.GetContractAddressByName(&bind.CallOpts{Context: ctx}, name)
		require.NoError(t, err)
		return a
	}
	addresses := config.Addresses{
		FlareSystemsManager: address("FlareSystemsManager"),
		Relay:               address("Relay"),
		VoterRegistry:       address("VoterRegistry"),
	}
	if r := os.Getenv(liveRelayVariable); r != "" {
		addresses.Relay = common.HexToAddress(r)
	}

	src, err := Dial(ctx, config.Chain{Source: config.ChainSourceRPC, URL: url, MaxLogsRange: 30, MaxBatchSize: 50, Timeout: 30 * time.Second}, addresses, id.Uint64(), database.SyncParams{})
	require.NoError(t, err)
	defer src.Close()

	hash, err := src.LatestBlockHash(ctx)
	require.NoError(t, err)
	var byHash *block
	require.NoError(t, src.rpc.CallContext(ctx, &byHash, "eth_getBlockByHash", hash, false))
	require.NotNil(t, byHash, "the raw head hash must name a real block")

	latest, err := src.LatestSigningPolicies(ctx, 1)
	require.NoError(t, err)
	require.Len(t, latest, 1)
	epoch := uint32(latest[0].RewardEpochId.Uint64())

	active, found, err := policy.FetchSigningPolicy(ctx, src, epoch-1)
	require.NoError(t, err)
	require.True(t, found)

	_, next, err := policy.UpdatePolicyAction(ctx, src, addresses, latest[0], active, new(big.Int).Set(id))
	require.NoError(t, err)
	require.Equal(t, epoch, next.RewardEpochID)
}
