package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"

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
