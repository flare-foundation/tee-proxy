package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/storage"

	"github.com/alicebob/miniredis/v2"
)

// makeSignedProof builds a valid SignedKeyExistenceProof for the given pair.
func makeSignedProof(t *testing.T, walletID common.Hash, keyID uint64) *wallets.SignedKeyExistenceProof {
	t.Helper()

	ar := makeKeyGenActionResult(t, walletID, keyID)
	var p wallets.SignedKeyExistenceProof
	require.NoError(t, json.Unmarshal(ar.Data, &p))
	return &p
}

// dequeueDirect polls the Direct queue until an action is available or ctx ends.
func dequeueDirect(ctx context.Context, aq *queue.ActionQueues) (*types.Action, error) {
	for {
		action, err := aq.Dequeue(ctx, processorutils.Direct)
		if err == nil {
			return action, nil
		}
		if !errors.Is(err, storage.ErrEmptyQueue) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// storeGetResponse posts a KEY_INFO/KEY_PROOF (op.Get) response for action into ResultStorage.
func storeGetResponse(ctx context.Context, rs *result.ResultStorage, action *types.Action, cmd op.Command, data []byte) error {
	return rs.StoreResponse(ctx, &types.ActionResponse{
		Result: types.ActionResult{
			ID:            action.Data.ID,
			SubmissionTag: action.Data.SubmissionTag,
			Status:        1,
			OPType:        op.Get.Hash(),
			OPCommand:     cmd.Hash(),
			Data:          data,
		},
	})
}

// TestSyncPreservesKeyAddedDuringSync guards the removeStaleKeys reconciliation: a fake
// tee-node gates the KEY_PROOF response so a key is added mid-sync, and that key — absent
// from the remote snapshot taken at sync start — must not be evicted as stale.
func TestSyncPreservesKeyAddedDuringSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	svc := NewService(aq, rs, nil, nil, time.Hour, nil)

	// K0 exists on the tee-node and needs a proof (local cache starts empty).
	k0Wallet := common.BytesToHash([]byte("wallet-K0"))
	k0Proof := makeSignedProof(t, k0Wallet, 0)
	remoteInfo := []types.KeyInfo{{WalletID: k0Wallet, KeyID: 0, Nonce: 0}}

	// K1 is created concurrently, mid-sync; it is NOT in the remote snapshot.
	k1Wallet := common.BytesToHash([]byte("wallet-K1"))
	k1Action := makeKeyGenActionResult(t, k1Wallet, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	proofReached := make(chan struct{})
	releaseProof := make(chan struct{})

	// Fake tee-node: answers KEY_INFO at once; gates KEY_PROOF on releaseProof to open
	// the snapshot→removeStaleKeys window.
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		for {
			action, err := dequeueDirect(ctx, aq)
			if err != nil {
				return
			}

			var di types.DirectInstruction
			if err := json.Unmarshal(action.Data.Message, &di); err != nil {
				t.Errorf("fake tee-node: unmarshal direct instruction: %v", err)
				return
			}

			switch di.OPCommand {
			case op.KeyInfo.Hash():
				data, err := json.Marshal(remoteInfo)
				if err != nil {
					t.Errorf("fake tee-node: marshal key info: %v", err)
					return
				}
				if err := storeGetResponse(ctx, rs, action, op.KeyInfo, data); err != nil {
					t.Errorf("fake tee-node: store key info response: %v", err)
					return
				}
			case op.KeyProof.Hash():
				close(proofReached)
				select {
				case <-releaseProof:
				case <-ctx.Done():
					return
				}
				data, err := json.Marshal([]*wallets.SignedKeyExistenceProof{k0Proof})
				if err != nil {
					t.Errorf("fake tee-node: marshal key proof: %v", err)
					return
				}
				if err := storeGetResponse(ctx, rs, action, op.KeyProof, data); err != nil {
					t.Errorf("fake tee-node: store key proof response: %v", err)
					return
				}
				return
			default:
				t.Errorf("fake tee-node: unexpected op command %v", di.OPCommand)
				return
			}
		}
	}()

	var syncErr error
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		syncErr = svc.sync(ctx)
	}()

	// Wait until sync is blocked on the KEY_PROOF response, then add K1 mid-sync.
	select {
	case <-proofReached:
	case <-ctx.Done():
		t.Fatal("sync never requested key proofs")
	}

	_, added, err := svc.update(k1Action)
	require.NoError(t, err)
	require.True(t, added)

	close(releaseProof)

	select {
	case <-syncDone:
	case <-ctx.Done():
		t.Fatal("sync did not finish")
	}
	require.NoError(t, syncErr)
	<-teeDone

	// Sanity: the remote key K0 was fetched and cached.
	_, err = svc.KeyData(k0Wallet, 0)
	require.NoError(t, err, "remote key K0 should be cached after sync")

	// K1 was added mid-sync and is absent from the remote snapshot; it must survive.
	_, err = svc.KeyData(k1Wallet, 0)
	require.NoError(t, err, "key added during sync must survive removeStaleKeys")
}

// walletSyncCount reads teeproxy_wallet_sync_total for the given result label from m's
// registry, returning 0 if no series with that label exists.
func walletSyncCount(t *testing.T, m *metrics.Metrics, result string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_wallet_sync_total" {
			continue
		}
		for _, mc := range f.GetMetric() {
			for _, l := range mc.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return mc.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

// TestSyncParseErrorIncrementsWalletSync guards fetchKeyInfo's parse-error accounting: a
// KEY_INFO response whose data does not decode as JSON must surface as a sync error and
// increment wallet_sync_total{result="parse_error"} exactly once, without touching
// enqueue_error or skipped.
func TestSyncParseErrorIncrementsWalletSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(aq, rs, nil, nil, time.Hour, m)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		action, err := dequeueDirect(ctx, aq)
		if err != nil {
			return
		}
		_ = storeGetResponse(ctx, rs, action, op.KeyInfo, []byte("not-a-json-array"))
	}()

	err := svc.sync(ctx)
	require.Error(t, err)

	require.Equal(t, float64(1), walletSyncCount(t, m, "parse_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "skipped"))
}

// TestSyncKeyProofParseErrorIncrementsWalletSync guards the batch loop's parse-error
// accounting: a KEY_PROOF response whose KeyExistence field fails to ABI-decode must
// surface as a sync error and increment wallet_sync_total{result="parse_error"}.
func TestSyncKeyProofParseErrorIncrementsWalletSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(aq, rs, nil, nil, time.Hour, m)

	k0Wallet := common.BytesToHash([]byte("wallet-K0-badproof"))
	remoteInfo := []types.KeyInfo{{WalletID: k0Wallet, KeyID: 0, Nonce: 0}}
	badProof := &wallets.SignedKeyExistenceProof{
		KeyExistence: hexutil.Bytes{0x01, 0x02, 0x03},
		Signature:    hexutil.Bytes{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		for {
			action, err := dequeueDirect(ctx, aq)
			if err != nil {
				return
			}

			var di types.DirectInstruction
			if err := json.Unmarshal(action.Data.Message, &di); err != nil {
				return
			}

			switch di.OPCommand {
			case op.KeyInfo.Hash():
				data, err := json.Marshal(remoteInfo)
				if err != nil {
					return
				}
				if err := storeGetResponse(ctx, rs, action, op.KeyInfo, data); err != nil {
					return
				}
			case op.KeyProof.Hash():
				data, err := json.Marshal([]*wallets.SignedKeyExistenceProof{badProof})
				if err != nil {
					return
				}
				_ = storeGetResponse(ctx, rs, action, op.KeyProof, data)
				return
			default:
				return
			}
		}
	}()

	err := svc.sync(ctx)
	require.Error(t, err)

	require.Equal(t, float64(1), walletSyncCount(t, m, "parse_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "skipped"))
}

// TestFetchKeyInfoEnqueueErrorIncrementsWalletSync guards fetchKeyInfo's enqueue-error
// accounting: an Enqueue failure (Redis unreachable) increments
// wallet_sync_total{result="enqueue_error"} exactly once.
func TestFetchKeyInfoEnqueueErrorIncrementsWalletSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(aq, rs, nil, nil, time.Hour, m)

	mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := svc.sync(ctx)
	require.Error(t, err)

	require.Equal(t, float64(1), walletSyncCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "parse_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "skipped"))
}

// TestFetchKeyInfoWaitErrorIncrementsWalletSync guards fetchKeyInfo's wait-error
// accounting: a node wait that runs out its deadline (no response ever stored) increments
// wallet_sync_total{result="wait_error"} exactly once, so the sync-failure alerts see the
// node-not-answering mode.
func TestFetchKeyInfoWaitErrorIncrementsWalletSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(aq, rs, nil, nil, time.Hour, m)

	// No fake node consumes the action, so the wait can only end on the ctx deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := svc.sync(ctx)
	require.Error(t, err)

	require.Equal(t, float64(1), walletSyncCount(t, m, "wait_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "parse_error"))
}

// TestFetchKeyProofsWaitErrorIncrementsWalletSync guards fetchKeyProofs' wait-error
// accounting: the fake node answers KEY_INFO then goes silent, so the KEY_PROOF wait runs
// out the ctx deadline and must record wallet_sync_total{result="wait_error"} exactly once.
func TestFetchKeyProofsWaitErrorIncrementsWalletSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(aq, rs, nil, nil, time.Hour, m)

	k0Wallet := common.BytesToHash([]byte("wallet-proof-wait-error"))
	remoteInfo := []types.KeyInfo{{WalletID: k0Wallet, KeyID: 0, Nonce: 0}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		for {
			action, err := dequeueDirect(ctx, aq)
			if err != nil {
				return
			}

			var di types.DirectInstruction
			if err := json.Unmarshal(action.Data.Message, &di); err != nil {
				return
			}
			if di.OPCommand != op.KeyInfo.Hash() {
				// The KEY_PROOF action is consumed but never answered.
				return
			}

			data, err := json.Marshal(remoteInfo)
			if err != nil {
				return
			}
			if err := storeGetResponse(ctx, rs, action, op.KeyInfo, data); err != nil {
				return
			}
		}
	}()

	err := svc.sync(ctx)
	require.Error(t, err)

	require.Equal(t, float64(1), walletSyncCount(t, m, "wait_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "parse_error"))
}

// TestTriggerSyncSkippedIncrementsWalletSync guards triggerSync's skip path: calling it
// while a sync is already in progress must not start a second sync, must leave syncing
// untouched, and must increment wallet_sync_total{result="skipped"} exactly once.
func TestTriggerSyncSkippedIncrementsWalletSync(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(nil, nil, nil, nil, time.Hour, m)

	svc.syncing.Store(true)

	svc.triggerSync(context.Background())

	require.Equal(t, float64(1), walletSyncCount(t, m, "skipped"))
	require.True(t, svc.syncing.Load(), "a skipped trigger must not disturb the in-progress sync's flag")
}

// TestTriggerSyncSuccessIncrementsWalletSync guards triggerSync's success path end to end:
// a clean sync via a fake tee-node eventually increments
// wallet_sync_total{result="success"} exactly once and no error/skipped label.
func TestTriggerSyncSuccessIncrementsWalletSync(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(aq, rs, nil, nil, time.Hour, m)

	k0Wallet := common.BytesToHash([]byte("wallet-trigger-success"))
	k0Proof := makeSignedProof(t, k0Wallet, 0)
	remoteInfo := []types.KeyInfo{{WalletID: k0Wallet, KeyID: 0, Nonce: 0}}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		for {
			action, err := dequeueDirect(ctx, aq)
			if err != nil {
				return
			}

			var di types.DirectInstruction
			if err := json.Unmarshal(action.Data.Message, &di); err != nil {
				return
			}

			switch di.OPCommand {
			case op.KeyInfo.Hash():
				data, err := json.Marshal(remoteInfo)
				if err != nil {
					return
				}
				if err := storeGetResponse(ctx, rs, action, op.KeyInfo, data); err != nil {
					return
				}
			case op.KeyProof.Hash():
				data, err := json.Marshal([]*wallets.SignedKeyExistenceProof{k0Proof})
				if err != nil {
					return
				}
				_ = storeGetResponse(ctx, rs, action, op.KeyProof, data)
				return
			default:
				return
			}
		}
	}()

	svc.triggerSync(ctx)

	require.Eventually(t, func() bool {
		return walletSyncCount(t, m, "success") == 1
	}, 5*time.Second, 10*time.Millisecond, "triggerSync's success accounting is set asynchronously from its goroutine")

	require.Equal(t, float64(0), walletSyncCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "parse_error"))
	require.Equal(t, float64(0), walletSyncCount(t, m, "skipped"))
}
