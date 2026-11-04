package jsonrpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	ttypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/chain/chaintest"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
)

const chainID = 14

func shortRetries(t *testing.T) {
	t.Helper()

	old := retryBudget
	retryBudget = 500 * time.Millisecond
	t.Cleanup(func() { retryBudget = old })
}

func chainConfig(url string) config.Chain {
	return config.Chain{Source: config.ChainSourceRPC, URL: url, MaxLogsRange: 30, MaxBatchSize: 50, Timeout: 500 * time.Millisecond}
}

func dial(t *testing.T, c *chaintest.Chain) *Source {
	t.Helper()

	src, err := Dial(context.Background(), chainConfig(c.Node.URL()), chaintest.Addresses, c.ChainID, database.SyncParams{})
	require.NoError(t, err)
	t.Cleanup(src.Close)

	return src
}

func TestDialChecks(t *testing.T) {
	shortRetries(t)

	t.Run("reads the relay's initial epoch", func(t *testing.T) {
		src := dial(t, chaintest.New(t, chainID, 5))
		require.Equal(t, uint32(5), src.initialEpoch)
	})

	t.Run("chain ID mismatch", func(t *testing.T) {
		c := chaintest.New(t, chainID, 5)
		_, err := Dial(context.Background(), chainConfig(c.Node.URL()), chaintest.Addresses, chainID+1, database.SyncParams{})
		require.ErrorIs(t, err, errChainIDMismatch)
	})

	t.Run("relay not fed by the FSM", func(t *testing.T) {
		c := chaintest.New(t, chainID, 5)
		relayABI, err := relay.RelayMetaData.GetAbi()
		require.NoError(t, err)
		c.Node.Handle(chaintest.Addresses.Relay, relayABI, "signingPolicySetter", func([]any) ([]any, error) {
			return []any{common.HexToAddress("0xbad")}, nil
		})

		_, err = Dial(context.Background(), chainConfig(c.Node.URL()), chaintest.Addresses, chainID, database.SyncParams{})
		require.ErrorIs(t, err, errRelaySetter)
	})

	t.Run("relay without initialRewardEpochId", func(t *testing.T) {
		c := chaintest.New(t, chainID, 5)
		c.Node.HandleRaw(chaintest.Addresses.Relay, initialRewardEpochIdSel, nil, chaintest.RevertError(nil))

		src := dial(t, c)
		require.Zero(t, src.initialEpoch)
	})

	t.Run("credentials kept out of errors", func(t *testing.T) {
		for _, endpoint := range []string{
			"http://127.0.0.1:1/secret-path",
			"http://127.0.0.1:bad/secret-path",
			"http://user:secret-pass@127.0.0.1:1/secret-path",
			"http://127.0.0.1:1/secret path",
			"http://127.0.0.1:1/secret-path\n",
		} {
			for _, key := range []string{"", "secret-key", "secret+key/="} {
				cfg := chainConfig(endpoint)
				cfg.APIKey = key
				_, err := Dial(context.Background(), cfg, chaintest.Addresses, chainID, database.SyncParams{})
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret", endpoint+" "+key)
			}
		}
	})
}

// TestDialSendsCredentials reaches a node that wants basic auth and the key as x-apikey,
// keeping the URL's own query.
func TestDialSendsCredentials(t *testing.T) {
	shortRetries(t)

	c := chaintest.New(t, chainID, 0)
	target, err := url.Parse(c.Node.URL())
	require.NoError(t, err)
	forward := httputil.NewSingleHostReverseProxy(target)
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		if q := r.URL.Query(); q.Get("x-apikey") != "k1" || q.Get("route") != "c" || user != "u" || password != "p" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(gate.Close)

	cfg := chainConfig(strings.Replace(gate.URL, "http://", "http://u:p@", 1) + "/?route=c")
	cfg.APIKey = "k1"
	src, err := Dial(context.Background(), cfg, chaintest.Addresses, chainID, database.SyncParams{})
	require.NoError(t, err)
	src.Close()

	cfg.APIKey = "wrong"
	_, err = Dial(context.Background(), cfg, chaintest.Addresses, chainID, database.SyncParams{})
	require.ErrorContains(t, err, "401")
}

func TestHeadReadsRawBlock(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	hash := common.HexToHash("0xc0ffee")
	c.Node.SetHead(120, 1_700_000_000, hash)
	src := dial(t, c)

	got, err := src.LatestBlockHash(context.Background())
	require.NoError(t, err)
	require.Equal(t, hash, got)

	number, timestamp, err := src.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(120), number)
	require.Equal(t, uint64(1_700_000_000), timestamp)
}

func TestWaitSynced(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	src := dial(t, c)
	src.sync = database.SyncParams{Retries: 2, MinSleepTime: time.Millisecond, MaxSleepTime: time.Millisecond, OutOfSyncTolerance: time.Minute}

	c.Node.SetHead(10, uint64(time.Now().Add(-time.Hour).Unix()))
	require.ErrorContains(t, src.WaitSynced(context.Background()), "out of sync")

	c.Node.SetHead(11, uint64(time.Now().Unix()))
	require.NoError(t, src.WaitSynced(context.Background()))
}

func TestFilterLogsChunksRanges(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	c.Node.MaxLogsRange = 30
	src := dial(t, c)

	address := common.HexToAddress("0x10")
	for _, block := range []uint64{1, 31, 95} {
		c.Node.AddLog(types.Log{Address: address, Topics: []common.Hash{{1}}, BlockNumber: block, TxHash: common.Hash{byte(block)}})
	}

	before := c.Node.Requests("eth_getLogs")
	logs, err := src.filterLogs(context.Background(), queryFor(address), 1, 100)
	require.NoError(t, err)
	require.Len(t, logs, 3)
	require.Equal(t, 4, c.Node.Requests("eth_getLogs")-before)
}

func TestSigningPolicy(t *testing.T) {
	shortRetries(t)

	c := chaintest.New(t, chainID, 5)
	keys := testutil.Keys(t, 2)
	c.AddPolicy(t, testutil.PolicyEvent(6, keys, []uint16{1, 1}, 1), 100)
	c.AddPolicy(t, testutil.PolicyEvent(7, keys, []uint16{1, 1}, 1), 200)
	src := dial(t, c)
	ctx := context.Background()

	calls := c.Node.Requests("eth_call")
	_, found, err := src.SigningPolicy(ctx, 5)
	require.NoError(t, err)
	require.False(t, found, "the relay's initial epoch has no event")
	require.Equal(t, calls, c.Node.Requests("eth_call"))

	event, found, err := src.SigningPolicy(ctx, 6)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(6), event.RewardEpochId.Uint64())
	require.Equal(t, uint64(100), event.Raw.BlockNumber)

	_, found, err = src.SigningPolicy(ctx, 8)
	require.NoError(t, err)
	require.False(t, found, "not initialized yet")

	c.SetSignInfo(8, 300, 0)
	_, found, err = src.SigningPolicy(ctx, 8)
	require.NoError(t, err)
	require.False(t, found, "initialized by the FSM on another relay")

	c.SetSignInfo(7, 250, 0)
	_, _, err = src.SigningPolicy(ctx, 7)
	require.ErrorIs(t, err, errPolicyEventMissing)
	c.SetSignInfo(7, 200, 0)

	events, err := src.LatestSigningPolicies(ctx, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, uint64(7), events[0].RewardEpochId.Uint64())

	events, err = src.LatestSigningPolicies(ctx, 5)
	require.NoError(t, err)
	require.Len(t, events, 2, "stops at the relay's initial epoch")
	require.Equal(t, uint64(6), events[1].RewardEpochId.Uint64())

	c.SetSignInfo(6, 150, 0)
	_, found, err = src.SigningPolicy(ctx, 6)
	require.NoError(t, err)
	require.False(t, found, "an older event the node no longer serves")
	events, err = src.LatestSigningPolicies(ctx, 5)
	require.NoError(t, err)
	require.Len(t, events, 1, "stops at the event the node no longer serves")
}

func TestVoterRegistrationsScansWindow(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	c.Node.MaxLogsRange = 30
	keys := testutil.Keys(t, 3)
	signers := []common.Address{crypto.PubkeyToAddress(keys[0].PublicKey), crypto.PubkeyToAddress(keys[1].PublicKey)}

	c.AddRegistration(t, 7, common.HexToAddress("0xa"), keys[0], keys[0], 150)
	c.AddRegistration(t, 7, common.HexToAddress("0xb"), keys[1], keys[1], 199)
	c.AddRegistration(t, 7, common.HexToAddress("0xc"), keys[2], keys[2], 160) // not asked for
	c.AddRegistration(t, 8, common.HexToAddress("0xa"), keys[0], keys[0], 170) // another epoch
	c.AddPolicy(t, testutil.PolicyEvent(7, keys, []uint16{1, 1, 1}, 1), 200)
	src := dial(t, c)

	got, err := src.VoterRegistrations(context.Background(), chaintest.Addresses.VoterRegistry, 7, signers)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, a := range signers {
		require.Len(t, got[a], 1)
		require.Equal(t, uint32(7), got[a][0].RewardEpochId)
	}

	_, err = src.VoterRegistrations(context.Background(), chaintest.Addresses.VoterRegistry, 9, signers)
	require.ErrorIs(t, err, errNoRegistrationWindow)
}

func TestPolicySignaturesWaitForSigningEnd(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	keys := testutil.Keys(t, 3)
	c.AddPolicy(t, testutil.PolicyEvent(7, keys, []uint16{1, 1, 1}, 1), 100)
	hash := common.HexToHash("0x77")
	c.SignPolicy(t, 7, hash, keys[0], 101, 1)
	c.SignPolicy(t, 7, hash, keys[1], 102, 0) // reverted: emits nothing
	c.SignPolicy(t, 7, hash, keys[2], 103, 1)
	// a SigningPolicySigned emitted by a call through another contract
	via := c.Node.AddTx(103, common.HexToAddress("0x5afe"), testutil.SignNewSigningPolicyInput(t, 7, hash, keys[1]))
	c.Node.AddLog(types.Log{Address: chaintest.Addresses.FlareSystemsManager, Topics: []common.Hash{signingPolicySignedEventSel, convert.Uint32ToHash(7)}, BlockNumber: 103, TxIndex: via, TxHash: common.Hash{9}})
	src := dial(t, c)

	inputs, err := src.PolicySignatures(context.Background(), 7, 0, 0)
	require.NoError(t, err)
	require.Empty(t, inputs, "signing has not ended")

	c.EndSigning(7, 103)
	inputs, err = src.PolicySignatures(context.Background(), 7, 0, 0)
	require.NoError(t, err)
	require.Equal(t, [][]byte{
		testutil.SignNewSigningPolicyInput(t, 7, hash, keys[0]),
		testutil.SignNewSigningPolicyInput(t, 7, hash, keys[2]),
	}, inputs)
}

// TestPolicySignaturesRetriesMissingTransaction fails rather than return a short set
// when the node does not have a signing transaction yet, as a lagging backend may.
func TestPolicySignaturesRetriesMissingTransaction(t *testing.T) {
	shortRetries(t)

	c := chaintest.New(t, chainID, 0)
	keys := testutil.Keys(t, 2)
	c.AddPolicy(t, testutil.PolicyEvent(7, keys, []uint16{1, 1}, 1), 100)
	hash := common.HexToHash("0x77")
	c.SignPolicy(t, 7, hash, keys[0], 101, 1)
	late := testutil.SignNewSigningPolicyInput(t, 7, hash, keys[1])
	c.Node.AddLog(types.Log{Address: chaintest.Addresses.FlareSystemsManager, Topics: []common.Hash{signingPolicySignedEventSel, convert.Uint32ToHash(7)}, BlockNumber: 102, TxIndex: 0, TxHash: common.Hash{2}})
	c.EndSigning(7, 102)
	src := dial(t, c)

	_, err := src.PolicySignatures(context.Background(), 7, 0, 0)
	require.ErrorIs(t, err, errTxMissing)

	c.Node.AddTx(102, chaintest.Addresses.FlareSystemsManager, late)
	inputs, err := src.PolicySignatures(context.Background(), 7, 0, 0)
	require.NoError(t, err)
	require.Equal(t, [][]byte{testutil.SignNewSigningPolicyInput(t, 7, hash, keys[0]), late}, inputs)
}

func TestPolicySignaturesBatchSize(t *testing.T) {
	shortRetries(t)

	c := chaintest.New(t, chainID, 0)
	c.Node.SetBatchLimit(2)
	keys := testutil.Keys(t, 3)
	c.AddPolicy(t, testutil.PolicyEvent(7, keys, []uint16{1, 1, 1}, 1), 100)
	hash := common.HexToHash("0x77")
	for i, k := range keys {
		c.SignPolicy(t, 7, hash, k, 101+uint64(i), 1)
	}
	c.EndSigning(7, 103)

	tests := []struct {
		size    int
		wantErr bool
	}{
		{size: 2},
		{size: 3, wantErr: true},
	}
	for _, tc := range tests {
		cfg := chainConfig(c.Node.URL())
		cfg.MaxBatchSize = tc.size
		src, err := Dial(context.Background(), cfg, chaintest.Addresses, chainID, database.SyncParams{})
		require.NoError(t, err)
		t.Cleanup(src.Close)

		inputs, err := src.PolicySignatures(context.Background(), 7, 0, 0)
		if tc.wantErr {
			require.Error(t, err, "batch of %d", tc.size)
			continue
		}
		require.NoError(t, err)
		require.Len(t, inputs, 3)
	}
}

func TestLatestSignedNonce(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	src := dial(t, c)
	ext := common.HexToHash("0x2a")
	ctx := context.Background()

	calls := c.Node.Requests("eth_call")
	_, found, err := src.LatestSignedNonce(ctx, ext, 0)
	require.NoError(t, err)
	require.False(t, found, "no list signed yet")
	require.Equal(t, calls+1, c.Node.Requests("eth_call"), "a revert is an answer, not retried")

	c.ActivateList(t, ext, 3, 100)
	nonce, found, err := src.LatestSignedNonce(ctx, ext, 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(3), nonce)

	_, found, err = src.LatestSignedNonce(ctx, ext, 3)
	require.NoError(t, err)
	require.False(t, found)
}

func TestSignedListRebuildsSafeApproval(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	ext := common.HexToHash("0x2a")
	const nonce = 4

	owners := testutil.OwnerAddresses(t, testutil.SafeOwnerKeys)
	govHash, err := ttypes.GovernanceHashSafe(common.HexToAddress("0x7ee"), common.HexToAddress("0x5afe"), owners, 2)
	require.NoError(t, err)
	gov := ttypes.Governance{Safe: common.HexToAddress("0x5afe"), Signers: owners, Threshold: 2, Hash: govHash}

	c.AddPaths(t, ext, nonce, []cmpaths.IMachinePathManagerMachinePath{testutil.MachinePath(common.HexToAddress("0x1"), common.HexToAddress("0x2"))}, 100)
	c.ApproveBySafe(t, gov, ext, nonce, testutil.SafeOwnerKeys[:2], 42, 105)
	c.ActivateList(t, ext, nonce, 105, govHash)
	src := dial(t, c)

	action, err := machinepath.SetMachinePathListAction(context.Background(), src, chaintest.Addresses.MachinePathManager, gov, ext, chainID, nonce)
	require.NoError(t, err)

	var di ttypes.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	var req ttypes.SetMachinePathListRequest
	require.NoError(t, json.Unmarshal(di.Message, &req))
	require.NotNil(t, req.SafeApproval)
	require.Equal(t, uint64(42), req.SafeApproval.Nonce)
	require.NoError(t, req.SafeApproval.Verify(chainID, gov.Safe, owners, 2))

	// a list that does not involve the node's governance carries no evidence for it
	other := gov
	other.Hash = common.Hash{1}
	_, err = machinepath.SetMachinePathListAction(context.Background(), src, chaintest.Addresses.MachinePathManager, other, ext, chainID, nonce)
	require.ErrorIs(t, err, machinepath.ErrNoAuthorization)
}

func TestRetriesTransientFailures(t *testing.T) {
	c := chaintest.New(t, chainID, 0)
	c.Node.SetHead(7, 1)
	src := dial(t, c)

	before := c.Node.Requests("eth_getBlockByNumber")
	c.Node.FailNext(2)
	number, _, err := src.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(7), number)
	require.Equal(t, 3, c.Node.Requests("eth_getBlockByNumber")-before)
}

// TestRetryAllowsOneFullTimeout lets a request use its whole timeout although that
// exceeds the retry budget.
func TestRetryAllowsOneFullTimeout(t *testing.T) {
	shortRetries(t)

	s := &Source{timeout: 2 * retryBudget}
	err := s.retry(context.Background(), func(ctx context.Context) error {
		select {
		case <-time.After(retryBudget + 200*time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	require.NoError(t, err)
}

func TestRedact(t *testing.T) {
	orig := stringError("Post \"https://node/x?x-apikey=k1\": refused (k1)")
	err := redact(orig, "k1", "https://node/x", "https://node/x?x-apikey=k1")
	require.Equal(t, "Post \"<redacted>\": refused (<redacted>)", err.Error())
	require.ErrorIs(t, err, orig)
	require.Equal(t, orig, redact(orig, "absent"))
	require.NoError(t, redact(nil, "x"))
}

type stringError string

func (e stringError) Error() string { return string(e) }

func queryFor(address common.Address) ethereum.FilterQuery {
	return ethereum.FilterQuery{Addresses: []common.Address{address}}
}
