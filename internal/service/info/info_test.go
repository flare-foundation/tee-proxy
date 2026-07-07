package info

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/attestation"

	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/storage"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/stretchr/testify/require"
)

// signedTeeInfoResponse builds an ActionResponse carrying resp, signed by key over the
// chain-bound TEE_ACTION_RESULT preimage and with key's public key embedded in resp.
// updateInfo verifies this signature against that embedded key before attestation, so a
// test response must carry a matching one.
func signedTeeInfoResponse(t *testing.T, key *ecdsa.PrivateKey, actionID common.Hash, tag types.SubmissionTag, resp *types.TeeInfoResponse) *types.ActionResponse {
	t.Helper()

	pub := types.PubKeyToStruct(&key.PublicKey)
	resp.TeeInfo.PublicKey = pub
	resp.MachineData.PublicKey = pub

	m, err := json.Marshal(resp)
	require.NoError(t, err)

	ar := &types.ActionResponse{
		Result: types.ActionResult{
			ID:            actionID,
			SubmissionTag: tag,
			Status:        1,
			OPType:        op.Get.Hash(),
			OPCommand:     op.TEEInfo.Hash(),
			Version:       "1.0.0",
			Data:          m,
		},
	}

	signHash, err := signing.NewPayload(signing.TEEActionResult, resp.TeeInfo.ChainID, [32]byte(ar.Result.Hash())).Hash()
	require.NoError(t, err)

	sig, err := crypto.Sign(accounts.TextHash(signHash[:]), key)
	require.NoError(t, err)
	ar.Signature = sig

	return ar
}

func TestInsertBlock(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "choose")
	err := db.AutoMigrate(&database.Block{})
	require.NoError(t, err)

	var latestBlockHash common.Hash
	for i := uint64(1); i <= 3; i++ {
		block, hash := testutil.CreateBlock(fmt.Sprintf("%d", i), i)
		latestBlockHash = hash
		err = db.Create(block).Error
		require.NoError(t, err)
	}

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)

	s := NewService(db, aq, rs, &config.InfoTiming{
		CycleInternal:          10 * time.Millisecond,
		CycleQueueResponseWait: 1 * time.Second,
	}, &attestation.Config{Enabled: false}, nil)

	go func() {
		err := s.Run(t.Context())
		require.Error(t, err)
	}()

	var a *types.Action
	require.Eventually(t, func() bool {
		var err error
		a, err = aq.Dequeue(t.Context(), processorutils.Direct)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, types.Submit, a.Data.SubmissionTag)
	require.Equal(t, types.Direct, a.Data.Type)

	var instruction types.DirectInstruction
	err = json.Unmarshal(a.Data.Message, &instruction)
	require.NoError(t, err)
	require.Equal(t, op.Get.Hash(), instruction.OPType)
	require.Equal(t, op.TEEInfo.Hash(), instruction.OPCommand)

	var data types.TeeInfoRequest
	err = json.Unmarshal(instruction.Message, &data)
	require.NoError(t, err)
	require.Equal(t, data.Challenge, latestBlockHash)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	resp := &types.TeeInfoResponse{
		TeeInfo: types.TeeInfo{
			Challenge: latestBlockHash,
			State:     types.TeeState{},
		},
		Attestation: "",
	}
	ar := signedTeeInfoResponse(t, key, a.Data.ID, a.Data.SubmissionTag, resp)

	err = rs.StoreResponse(t.Context(), ar)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		s.RLock()
		defer s.RUnlock()
		return s.Latest.TeeInfo.Challenge == latestBlockHash
	}, 2*time.Second, 10*time.Millisecond)
}

// TestAttestationStickyError checks that a verify failure during updateInfo
// sets lastAttestationErr and does not update Latest. The error must persist
// so that liveness.Ready surfaces it until the pod is restarted.
func TestAttestationStickyError(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "sticky")
	err := db.AutoMigrate(&database.Block{})
	require.NoError(t, err)

	var latestBlockHash common.Hash
	for i := uint64(1); i <= 3; i++ {
		block, hash := testutil.CreateBlock(fmt.Sprintf("%d", i), i)
		latestBlockHash = hash
		err = db.Create(block).Error
		require.NoError(t, err)
	}

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)

	// Enabled=true with AllowMagicPass=false rejects the magic_pass response below.
	s := NewService(db, aq, rs, &config.InfoTiming{
		CycleInternal:          10 * time.Millisecond,
		CycleQueueResponseWait: 1 * time.Second,
	}, &attestation.Config{Enabled: true, AllowMagicPass: false}, nil)

	require.NoError(t, s.LastAttestationErr())

	go func() {
		_ = s.Run(t.Context())
	}()

	var a *types.Action
	require.Eventually(t, func() bool {
		var err error
		a, err = aq.Dequeue(t.Context(), processorutils.Direct)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	resp := &types.TeeInfoResponse{
		TeeInfo:     types.TeeInfo{Challenge: latestBlockHash},
		Attestation: "magic_pass",
	}
	ar := signedTeeInfoResponse(t, key, a.Data.ID, a.Data.SubmissionTag, resp)

	require.NoError(t, rs.StoreResponse(t.Context(), ar))

	require.Eventually(t, func() bool {
		return s.LastAttestationErr() != nil
	}, 2*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, s.LastAttestationErr(), attestation.ErrMagicPassDisabled)

	// Latest must remain unwritten after a verification failure.
	s.RLock()
	require.Equal(t, common.Hash{}, s.Latest.TeeInfo.Challenge)
	s.RUnlock()
}
