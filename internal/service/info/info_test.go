package info

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
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

// TestAttestationOutcome checks the metric-label mapping: an accepted magic_pass sentinel
// is reported as ok/magic_pass (distinct from a genuine JWT pass), a rejected magic_pass and
// other verification failures take the error branch with their classified reason.
func TestAttestationOutcome(t *testing.T) {
	tests := []struct {
		name        string
		attestation string
		vErr        error
		wantResult  string
		wantReason  string
	}{
		{"accepted magic_pass", "magic_pass", nil, "ok", "magic_pass"},
		{"genuine jwt pass", "", nil, "ok", "ok"},
		{"rejected magic_pass", "magic_pass", attestation.ErrMagicPassDisabled, "error", "magic_pass_disabled"},
		{"pubkey mismatch", "magic_pass", attestation.ErrPubKeyMismatch, "error", "pubkey_mismatch"},
		{"unclassified error", "", errors.New("boom"), "error", "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tir := &types.TeeInfoResponse{Attestation: tt.attestation}
			res, reason := attestationOutcome(tir, tt.vErr)
			require.Equal(t, tt.wantResult, res)
			require.Equal(t, tt.wantReason, reason)
		})
	}
}

// TestNewServiceLastUpdatedStartsNow guards against reverting LastUpdated to the
// time.Unix(0, 0) sentinel, which made info_service_delay_seconds report decades
// of staleness until the first successful refresh.
func TestNewServiceLastUpdatedStartsNow(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "last-updated")

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)

	s := NewService(db, aq, rs, &config.InfoTiming{
		CycleInternal:          10 * time.Millisecond,
		CycleQueueResponseWait: 1 * time.Second,
	}, &attestation.Config{Enabled: false}, nil)

	require.WithinDuration(t, time.Now(), s.LastUpdated, time.Second)
}

// infoFailureCount reads teeproxy_info_refresh_failures_total for the given stage label
// from m's registry, returning 0 if no series with that label exists.
func infoFailureCount(t *testing.T, m *metrics.Metrics, stage string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_info_refresh_failures_total" {
			continue
		}
		for _, mc := range f.GetMetric() {
			for _, l := range mc.GetLabel() {
				if l.GetName() == "stage" && l.GetValue() == stage {
					return mc.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

// nodeWaitCount reads teeproxy_node_response_wait_total for the given path/result label
// pair from m's registry, returning 0 if no matching series exists.
func nodeWaitCount(t *testing.T, m *metrics.Metrics, path, result string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_node_response_wait_total" {
			continue
		}
		for _, mc := range f.GetMetric() {
			var gotPath, gotResult string
			for _, l := range mc.GetLabel() {
				switch l.GetName() {
				case "path":
					gotPath = l.GetValue()
				case "result":
					gotResult = l.GetValue()
				}
			}
			if gotPath == path && gotResult == result {
				return mc.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// attestationVerifyCount reads teeproxy_attestation_verify_total for the given
// result/reason label pair from m's registry, returning 0 if no matching series exists.
func attestationVerifyCount(t *testing.T, m *metrics.Metrics, result, reason string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_attestation_verify_total" {
			continue
		}
		for _, mc := range f.GetMetric() {
			var gotResult, gotReason string
			for _, l := range mc.GetLabel() {
				switch l.GetName() {
				case "result":
					gotResult = l.GetValue()
				case "reason":
					gotReason = l.GetValue()
				}
			}
			if gotResult == result && gotReason == reason {
				return mc.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// newInfoServiceWithBlock builds an info Service wired to a database with exactly one
// Block row (so FetchLatestBlock succeeds) and a live queue/result-storage pair, under ac.
func newInfoServiceWithBlock(t *testing.T, m *metrics.Metrics, ac *attestation.Config) (*Service, *queue.ActionQueues, *result.ResultStorage) {
	t.Helper()

	db, _ := testutil.InMemoryDB(t, "info-stage")
	require.NoError(t, db.AutoMigrate(&database.Block{}))
	block, _ := testutil.CreateBlock("prev", 1)
	require.NoError(t, db.Create(block).Error)

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)

	tc := &config.InfoTiming{CycleInternal: time.Hour, CycleQueueResponseWait: time.Second}

	return NewService(db, aq, rs, tc, ac, m), aq, rs
}

// runInfoRefresh runs updateInfo against svc in the background and returns the TEE_INFO
// action it enqueued (decoding the challenge it carries) along with a channel receiving
// updateInfo's returned error once it completes. The caller drives a specific failure stage
// by answering (or not answering) the action via rs.StoreResponse.
func runInfoRefresh(t *testing.T, svc *Service, aq *queue.ActionQueues) (action *types.Action, challenge common.Hash, errCh <-chan error) {
	t.Helper()

	ch := make(chan error, 1)
	go func() {
		_, err := svc.updateInfo(context.Background(), 2*time.Second)
		ch <- err
	}()

	require.Eventually(t, func() bool {
		var err error
		action, err = aq.Dequeue(context.Background(), processorutils.Direct)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	var req types.TeeInfoRequest
	require.NoError(t, json.Unmarshal(di.Message, &req))

	return action, req.Challenge, ch
}

// waitInfoRefresh blocks for updateInfo's returned error, failing the test if it never arrives.
func waitInfoRefresh(t *testing.T, errCh <-chan error) error {
	t.Helper()

	select {
	case err := <-errCh:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("updateInfo did not return")
		return nil
	}
}

// TestUpdateInfoStageLabels drives updateInfo down each reachable failure stage with a real
// metrics object and asserts the bounded info_refresh_failures_total{stage=...} label it
// selects. Two of the ten stages are intentionally not exercised: create_action
// (json.Marshal of TeeInfoRequest{Challenge} has no reachable failure mode) and signing_hash
// (signing.NewPayload(...).Hash() has no failure path exercisable without invasive mocking of
// the signing package).
func TestUpdateInfoStageLabels(t *testing.T) {
	tests := []struct {
		name       string
		wantStage  string
		otherStage string
		run        func(t *testing.T, m *metrics.Metrics)
	}{
		{
			name:       "fetch_block",
			wantStage:  "fetch_block",
			otherStage: "enqueue",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				db, _ := testutil.InMemoryDB(t, "stage-fetch-block")
				require.NoError(t, db.AutoMigrate(&database.Block{})) // no rows: FetchLatestBlock errors

				mr := miniredis.RunT(t)
				c := storage.NewClient(mr.Addr())
				aq := queue.NewActionQueues(c, time.Hour, nil)
				rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)
				tc := &config.InfoTiming{CycleInternal: time.Hour, CycleQueueResponseWait: time.Second}
				svc := NewService(db, aq, rs, tc, &attestation.Config{Enabled: false}, m)

				// FetchLatestBlock retries a genuine "no blocks" error under an exponential
				// backoff (up to go-flare-common's 15s maxQueryDuration); bound the context so
				// the retry loop aborts quickly instead of running the test for the full window.
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				_, err := svc.updateInfo(ctx, time.Second)
				require.Error(t, err)
			},
		},
		{
			name:       "enqueue",
			wantStage:  "enqueue",
			otherStage: "fetch_block",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				db, _ := testutil.InMemoryDB(t, "stage-enqueue")
				require.NoError(t, db.AutoMigrate(&database.Block{}))
				block, _ := testutil.CreateBlock("prev", 1)
				require.NoError(t, db.Create(block).Error)

				mr := miniredis.RunT(t)
				c := storage.NewClient(mr.Addr())
				aq := queue.NewActionQueues(c, time.Hour, nil)
				rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)
				tc := &config.InfoTiming{CycleInternal: time.Hour, CycleQueueResponseWait: time.Second}
				svc := NewService(db, aq, rs, tc, &attestation.Config{Enabled: false}, m)

				mr.Close()

				_, err := svc.updateInfo(context.Background(), time.Second)
				require.Error(t, err)
			},
		},
		{
			name:       "wait_response",
			wantStage:  "wait_response",
			otherStage: "action_status",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				svc, _, _ := newInfoServiceWithBlock(t, m, &attestation.Config{Enabled: false})

				_, err := svc.updateInfo(context.Background(), 20*time.Millisecond)
				require.Error(t, err)

				// A WaitOnResponse expiry surfaces as a net read-deadline error (go-redis turns
				// the context deadline into a raw net.Conn deadline), which nodeWaitResult
				// classifies as "timeout" alongside context.DeadlineExceeded.
				require.Equal(t, float64(1), nodeWaitCount(t, m, "info", "timeout"))
				require.Equal(t, float64(0), nodeWaitCount(t, m, "info", "error"))
			},
		},
		{
			name:       "action_status",
			wantStage:  "action_status",
			otherStage: "unmarshal",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				svc, aq, rs := newInfoServiceWithBlock(t, m, &attestation.Config{Enabled: false})
				action, _, errCh := runInfoRefresh(t, svc, aq)

				key, err := crypto.GenerateKey()
				require.NoError(t, err)
				resp := &types.TeeInfoResponse{}
				ar := signedTeeInfoResponse(t, key, action.Data.ID, action.Data.SubmissionTag, resp)
				ar.Result.Status = 0

				require.NoError(t, rs.StoreResponse(context.Background(), ar))
				require.Error(t, waitInfoRefresh(t, errCh))
			},
		},
		{
			name:       "unmarshal",
			wantStage:  "unmarshal",
			otherStage: "parse_tee_id",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				svc, aq, rs := newInfoServiceWithBlock(t, m, &attestation.Config{Enabled: false})
				action, _, errCh := runInfoRefresh(t, svc, aq)

				ar := &types.ActionResponse{
					Result: types.ActionResult{
						ID:            action.Data.ID,
						SubmissionTag: action.Data.SubmissionTag,
						Status:        1,
						OPType:        op.Get.Hash(),
						OPCommand:     op.TEEInfo.Hash(),
						Version:       "1.0.0",
						Data:          []byte("not json"),
					},
				}

				require.NoError(t, rs.StoreResponse(context.Background(), ar))
				require.Error(t, waitInfoRefresh(t, errCh))
			},
		},
		{
			// attestationCfg is non-nil with Enabled false: the verify-signature block runs
			// whenever attestationCfg != nil, independent of Enabled.
			name:       "parse_tee_id",
			wantStage:  "parse_tee_id",
			otherStage: "verify_signature",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				svc, aq, rs := newInfoServiceWithBlock(t, m, &attestation.Config{Enabled: false})
				action, challenge, errCh := runInfoRefresh(t, svc, aq)

				// TeeInfo.PublicKey is left at its zero value: (0, 0) is not on the secp256k1
				// curve, so types.ParsePubKey rejects it.
				resp := &types.TeeInfoResponse{TeeInfo: types.TeeInfo{Challenge: challenge}}
				data, err := json.Marshal(resp)
				require.NoError(t, err)

				ar := &types.ActionResponse{
					Result: types.ActionResult{
						ID:            action.Data.ID,
						SubmissionTag: action.Data.SubmissionTag,
						Status:        1,
						OPType:        op.Get.Hash(),
						OPCommand:     op.TEEInfo.Hash(),
						Version:       "1.0.0",
						Data:          data,
					},
				}

				require.NoError(t, rs.StoreResponse(context.Background(), ar))
				require.Error(t, waitInfoRefresh(t, errCh))
			},
		},
		{
			name:       "verify_signature",
			wantStage:  "verify_signature",
			otherStage: "verify_attestation",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				svc, aq, rs := newInfoServiceWithBlock(t, m, &attestation.Config{Enabled: false})
				action, challenge, errCh := runInfoRefresh(t, svc, aq)

				key, err := crypto.GenerateKey()
				require.NoError(t, err)
				resp := &types.TeeInfoResponse{TeeInfo: types.TeeInfo{Challenge: challenge}}
				ar := signedTeeInfoResponse(t, key, action.Data.ID, action.Data.SubmissionTag, resp)
				ar.Signature[0] ^= 0xFF // corrupt the signature relative to the embedded pubkey

				require.NoError(t, rs.StoreResponse(context.Background(), ar))
				require.Error(t, waitInfoRefresh(t, errCh))
			},
		},
		{
			name:       "verify_attestation",
			wantStage:  "verify_attestation",
			otherStage: "fetch_block",
			run: func(t *testing.T, m *metrics.Metrics) {
				t.Helper()

				svc, aq, rs := newInfoServiceWithBlock(t, m, &attestation.Config{Enabled: true, AllowMagicPass: false})
				action, challenge, errCh := runInfoRefresh(t, svc, aq)

				key, err := crypto.GenerateKey()
				require.NoError(t, err)
				resp := &types.TeeInfoResponse{
					TeeInfo:     types.TeeInfo{Challenge: challenge},
					Attestation: "magic_pass",
				}
				ar := signedTeeInfoResponse(t, key, action.Data.ID, action.Data.SubmissionTag, resp)

				require.NoError(t, rs.StoreResponse(context.Background(), ar))
				require.Error(t, waitInfoRefresh(t, errCh))

				require.Equal(t, float64(1),
					attestationVerifyCount(t, m, "error", attestation.Reason(attestation.ErrMagicPassDisabled)))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := metrics.New(metrics.Config{Enable: true, Info: true, Attestation: true, Node: true})

			tc.run(t, m)

			require.Equal(t, float64(1), infoFailureCount(t, m, tc.wantStage),
				"stage %q must be incremented exactly once", tc.wantStage)
			require.Equal(t, float64(0), infoFailureCount(t, m, tc.otherStage),
				"stage %q must not be incremented", tc.otherStage)
		})
	}
}
