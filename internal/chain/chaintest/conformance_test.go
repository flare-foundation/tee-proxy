package chaintest_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/chain/chaintest"
	"github.com/flare-foundation/tee-proxy/internal/chain/indexer"
	"github.com/flare-foundation/tee-proxy/internal/chain/jsonrpc"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

const chainID = 14

type backend interface {
	policy.Source
	machinepath.Source
}

// backends reads c through both chain sources.
func backends(t *testing.T, c *chaintest.Chain) map[string]backend {
	t.Helper()

	node, err := jsonrpc.Dial(context.Background(), config.Chain{
		Source: config.ChainSourceRPC, URL: c.Node.URL(), MaxLogsRange: 30, MaxBatchSize: 50, Timeout: time.Second,
	}, chaintest.Addresses, chainID, database.SyncParams{})
	require.NoError(t, err)
	t.Cleanup(node.Close)

	return map[string]backend{
		"indexer": indexer.New(c.DB, chaintest.Addresses, database.SyncParams{}),
		"rpc":     node,
	}
}

func newKeys(t *testing.T, n int) []*ecdsa.PrivateKey {
	t.Helper()

	keys := make([]*ecdsa.PrivateKey, n)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}

	return keys
}

func identity(k *ecdsa.PrivateKey) common.Address {
	a := crypto.PubkeyToAddress(k.PublicKey)
	return common.BytesToAddress(crypto.Keccak256(a[:]))
}

// directRequest unpacks the request carried by a direct action.
func directRequest(t *testing.T, action *types.Action) json.RawMessage {
	t.Helper()

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))

	return json.RawMessage(di.Message)
}

// TestPolicyConformance runs a fresh start, a restart lookup and a rollover over
// one history; both sources must give the TEE node identical requests.
func TestPolicyConformance(t *testing.T) {
	c := chaintest.New(t, chainID, 4)
	keys := newKeys(t, 3)
	weights := []uint16{300, 300, 600}

	events := map[uint32]*cpolicy.SigningPolicy{}
	for i, epoch := range []uint32{5, 6, 7} {
		start := uint64(1000 * (i + 1))
		for j, k := range keys {
			c.AddRegistration(t, epoch, identity(k), k, k, start-50+uint64(j))
		}
		ev := testutil.PolicyEvent(epoch, keys, weights, 500)
		c.AddPolicy(t, ev, start)

		p, err := cpolicy.NewSigningPolicy(ev, nil)
		require.NoError(t, err)
		events[epoch] = p
	}

	// epoch 6 voters sign epoch 7: keys[0] and keys[1] together pass the threshold
	hash := common.BytesToHash(events[7].ChainBoundHash(chainID))
	c.SignPolicy(t, 7, hash, keys[2], 3001, 0) // reverted
	c.SignPolicy(t, 7, hash, keys[0], 3002, 1)
	c.SignPolicy(t, 7, hash, keys[1], 3004, 1)
	c.EndSigning(7, 3004)
	c.SetHead(t, 3010, uint64(time.Now().Unix()))

	type result struct {
		InitEpoch  uint32
		InitOffset int
		Init       json.RawMessage
		Restart    []uint32
		Update     json.RawMessage
	}
	results := map[string]result{}
	ctx := context.Background()

	for name, src := range backends(t, c) {
		var r result

		action, p, offset, err := policy.InitializePolicyAction(ctx, src, chaintest.Addresses, 1, big.NewInt(chainID))
		require.NoError(t, err, name)
		r.InitEpoch, r.InitOffset, r.Init = p.RewardEpochID, offset, directRequest(t, action)

		for epoch := uint32(4); epoch <= 8; epoch++ {
			_, found, err := policy.FetchSigningPolicy(ctx, src, epoch)
			require.NoError(t, err, name)
			if found {
				r.Restart = append(r.Restart, epoch)
			}
		}

		event, found, err := policy.FetchSigningPolicyEvent(ctx, src, 7)
		require.NoError(t, err, name)
		require.True(t, found, name)
		action, _, err = policy.UpdatePolicyAction(ctx, src, chaintest.Addresses, event, events[6], big.NewInt(chainID))
		require.NoError(t, err, name)
		r.Update = directRequest(t, action)

		results[name] = r
	}

	require.Equal(t, uint32(6), results["indexer"].InitEpoch)
	require.Equal(t, []uint32{5, 6, 7}, results["indexer"].Restart)
	require.Equal(t, results["indexer"], results["rpc"])
}

// TestPolicyConformanceShallowHistory boots where the oldest policy's event is no
// longer served; both sources must skip it alike.
func TestPolicyConformanceShallowHistory(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	keys := newKeys(t, 2)
	c.SetSignInfo(5, 500, 0) // recorded by the FSM, event gone from both stores
	for i, epoch := range []uint32{6, 7} {
		start := uint64(1000 * (i + 1))
		for j, k := range keys {
			c.AddRegistration(t, epoch, identity(k), k, k, start-50+uint64(j))
		}
		c.AddPolicy(t, testutil.PolicyEvent(epoch, keys, []uint16{1, 1}, 1), start)
	}
	c.SetHead(t, 2010, uint64(time.Now().Unix()))

	type result struct {
		InitEpoch  uint32
		InitOffset int
		Init       json.RawMessage
		Restart    []uint32
	}
	results := map[string]result{}
	ctx := context.Background()

	for name, src := range backends(t, c) {
		var r result

		action, p, offset, err := policy.InitializePolicyAction(ctx, src, chaintest.Addresses, 2, big.NewInt(chainID))
		require.NoError(t, err, name)
		r.InitEpoch, r.InitOffset, r.Init = p.RewardEpochID, offset, directRequest(t, action)

		for epoch := uint32(5); epoch <= 7; epoch++ {
			_, found, err := policy.FetchSigningPolicy(ctx, src, epoch)
			require.NoError(t, err, name)
			if found {
				r.Restart = append(r.Restart, epoch)
			}
		}

		results[name] = r
	}

	require.Equal(t, uint32(6), results["indexer"].InitEpoch)
	require.Equal(t, 1, results["indexer"].InitOffset)
	require.Equal(t, []uint32{6, 7}, results["indexer"].Restart)
	require.Equal(t, results["indexer"], results["rpc"])
}

// TestMachinePathConformance builds the action for a governance-signed list and a
// Safe-approved one; both sources must give the TEE node identical requests.
func TestMachinePathConformance(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	ext := common.HexToHash("0x2a")

	signerKeys := newKeys(t, 2)
	signers := []common.Address{crypto.PubkeyToAddress(signerKeys[0].PublicKey), crypto.PubkeyToAddress(signerKeys[1].PublicKey)}
	signedPaths := []cmpaths.IMachinePathManagerMachinePath{
		testutil.MachinePath(common.HexToAddress("0xa0"), common.HexToAddress("0xb0")),
		testutil.MachinePath(common.HexToAddress("0xa1"), common.HexToAddress("0xb1")),
	}
	c.AddPaths(t, ext, 1, signedPaths[:1], 100)
	c.AddPaths(t, ext, 1, signedPaths[1:], 101)
	hash := testutil.MachinePathListHash(t, chainID, ext, 1, signedPaths)
	c.SignList(t, ext, 1, testutil.GovernanceSignature(t, hash, signerKeys[0]), 102, 1)
	c.SignList(t, ext, 1, testutil.GovernanceSignature(t, hash, signerKeys[1]), 103, 1)
	c.ActivateList(t, ext, 1, 103)

	owners := testutil.OwnerAddresses(t, testutil.SafeOwnerKeys)
	safeAddress := common.HexToAddress("0x5afe")
	govHash, err := types.GovernanceHashSafe(common.HexToAddress("0x7ee"), safeAddress, owners, 2)
	require.NoError(t, err)
	safeGov := types.Governance{Safe: safeAddress, Signers: owners, Threshold: 2, Hash: govHash}
	c.AddPaths(t, ext, 2, []cmpaths.IMachinePathManagerMachinePath{testutil.MachinePath(common.HexToAddress("0xa2"), common.HexToAddress("0xb2"))}, 200)
	c.ApproveBySafe(t, safeGov, ext, 2, testutil.SafeOwnerKeys[:2], 7, 205)
	c.ActivateList(t, ext, 2, 205, govHash)

	type result struct {
		Latest   uint64
		Signed   json.RawMessage
		Approved json.RawMessage
	}
	results := map[string]result{}
	ctx := context.Background()

	for name, src := range backends(t, c) {
		var r result

		latest, found, err := src.LatestSignedNonce(ctx, ext, 0)
		require.NoError(t, err, name)
		require.True(t, found, name)
		r.Latest = latest

		action, err := machinepath.SetMachinePathListAction(ctx, src, chaintest.Addresses.MachinePathManager, types.Governance{Signers: signers, Threshold: 2}, ext, chainID, 1)
		require.NoError(t, err, name)
		r.Signed = directRequest(t, action)

		action, err = machinepath.SetMachinePathListAction(ctx, src, chaintest.Addresses.MachinePathManager, safeGov, ext, chainID, 2)
		require.NoError(t, err, name)
		r.Approved = directRequest(t, action)

		results[name] = r
	}

	require.Equal(t, uint64(2), results["indexer"].Latest)
	require.JSONEq(t, string(results["indexer"].Signed), string(results["rpc"].Signed))
	require.JSONEq(t, string(results["indexer"].Approved), string(results["rpc"].Approved))
	require.Equal(t, results["indexer"].Latest, results["rpc"].Latest)
}
