package result

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/random"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"github.com/stretchr/testify/require"
)

// TestLastStorageErrSetAndClear covers the auto-recovery contract: the most recent
// storage outcome wins, so a successful store clears the prior error and liveness
// recovers without a pod restart.
func TestLastStorageErrSetAndClear(t *testing.T) {
	s := NewService(nil, uint64(14), nil)

	require.NoError(t, s.LastStorageErr())

	boom := errors.New("redis down")
	s.recordStorageResult(boom)
	require.ErrorIs(t, s.LastStorageErr(), boom)

	s.recordStorageResult(nil)
	require.NoError(t, s.LastStorageErr())
}

// TestRecoverSignerChainIDBinding verifies that the TEE action-result signer is recovered
// against the chain-bound TEE_ACTION_RESULT preimage: a signature produced under one chain ID
// recovers to the signer under that chain ID and to a different address under another.
func TestRecoverSignerChainIDBinding(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	teeAddr := crypto.PubkeyToAddress(key.PublicKey)

	const signChainID = uint64(14)

	ar := &types.ActionResponse{
		Result: types.ActionResult{
			ID:            common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa"),
			SubmissionTag: types.Threshold,
			Status:        1,
			Data:          []byte(`{"ok":true}`),
		},
	}

	signHash, err := csigning.NewPayload(csigning.TEEActionResult, signChainID, common.BytesToHash(ar.Result.Hash())).Hash()
	require.NoError(t, err)
	sig, err := crypto.Sign(accounts.TextHash(signHash[:]), key)
	require.NoError(t, err)
	ar.Signature = sig

	got, err := recoverSigner(ar, signChainID)
	require.NoError(t, err)
	require.Equal(t, teeAddr, got,
		"signer must recover under the chain ID the signature was produced with")

	other, err := recoverSigner(ar, signChainID+1)
	require.NoError(t, err)
	require.NotEqual(t, teeAddr, other,
		"a result signed under one chain ID must not recover to the signer under another")
}

// TestProcessAndStoreDropsZeroIDResult verifies that a delivery-failure
// notification from the node (zero action ID) is dropped without error and
// without reaching storage, so it cannot collide on the zero key or surface as
// a spurious failed-result error during a proxy restart. The nil ResultStorage
// would nil-panic if the guard let execution fall through to a store.
func TestProcessAndStoreDropsZeroIDResult(t *testing.T) {
	s := NewService(nil, uint64(14), nil)

	r := &types.ActionResponse{
		Result: types.ActionResult{
			ID:     common.Hash{},
			Status: 0,
			Log:    "error posting result: unexpected status code: 503",
		},
	}

	require.NoError(t, s.ProcessAndStore(context.Background(), r))
}

// TestProcessAndStoreResultMetrics pins which counter ProcessAndStore increments on
// each outcome and that only a genuine persistence failure trips the liveness signal.
// It guards the override-rejection class distinction: a deliberate override-guard
// rejection must not inflate results_lost_total nor flap readiness, while a real
// backend failure must do both.
func TestProcessAndStoreResultMetrics(t *testing.T) {
	const (
		lost      = "teeproxy_results_lost_total"
		discarded = "teeproxy_results_discarded_total"
		processed = "teeproxy_results_processed_total"
		rejected  = "teeproxy_results_rejected_total"
	)
	mr := miniredis.RunT(t)
	n := storage.NewNotifier(storage.NewClient(mr.Addr()))

	t.Run("zero action ID is discarded, not lost", func(t *testing.T) {
		m := metrics.New(metrics.Config{Enable: true, Result: true})
		s := NewService(nil, uint64(14), m)

		require.NoError(t, s.ProcessAndStore(t.Context(), &types.ActionResponse{
			Result: types.ActionResult{ID: common.Hash{}, Status: 0},
		}))

		require.Equal(t, float64(1), counterValue(t, m, discarded))
		require.Equal(t, float64(0), counterValue(t, m, lost))
	})

	t.Run("non-TEE_INFO before identity is rejected as bootstrap, not lost", func(t *testing.T) {
		m := metrics.New(metrics.Config{Enable: true, Result: true})
		s := NewService(nil, uint64(14), m)

		id, err := random.Hash()
		require.NoError(t, err)
		err = s.ProcessAndStore(t.Context(), &types.ActionResponse{
			Result: types.ActionResult{ID: id, Status: 1, OPCommand: op.KeyGenerate.Hash()},
		})
		require.ErrorIs(t, err, errBootstrapNotTeeInfo)

		require.Equal(t, float64(1), counterValue(t, m, rejected))
		require.Equal(t, float64(0), counterValue(t, m, lost))
	})

	t.Run("successful store is processed, not lost, leaves liveness clean", func(t *testing.T) {
		m := metrics.New(metrics.Config{Enable: true, Result: true})
		rs := NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, 30*time.Minute)
		s := NewService(rs, uint64(14), m)

		id, err := random.Hash()
		require.NoError(t, err)
		require.NoError(t, s.ProcessAndStore(t.Context(), teeInfoResponse(id, types.Threshold)))

		require.Equal(t, float64(1), counterValue(t, m, processed))
		require.Equal(t, float64(0), counterValue(t, m, lost))
		require.NoError(t, s.LastStorageErr())
	})

	t.Run("override-guard rejection is not lost and does not flap liveness", func(t *testing.T) {
		m := metrics.New(metrics.Config{Enable: true, Result: true})
		rs := NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, 30*time.Minute)
		s := NewService(rs, uint64(14), m)

		id, err := random.Hash()
		require.NoError(t, err)
		// First store of a final result succeeds.
		require.NoError(t, s.ProcessAndStore(t.Context(), teeInfoResponse(id, types.End)))
		// A re-delivery under the same key is rejected by the override guard.
		err = s.ProcessAndStore(t.Context(), teeInfoResponse(id, types.End))
		require.ErrorContains(t, err, "override final status")

		require.Equal(t, float64(0), counterValue(t, m, lost),
			"a benign override rejection must not be counted as a lost result")
		require.NoError(t, s.LastStorageErr(),
			"a benign override rejection must not fail readiness")
	})

	t.Run("genuine storage failure is lost and fails readiness", func(t *testing.T) {
		m := metrics.New(metrics.Config{Enable: true, Result: true})
		rs := NewStorage(failingStorage{}, n, time.Hour, 30*time.Minute)
		s := NewService(rs, uint64(14), m)

		id, err := random.Hash()
		require.NoError(t, err)
		err = s.ProcessAndStore(t.Context(), teeInfoResponse(id, types.Threshold))
		require.ErrorIs(t, err, errStoreFailed)

		require.Equal(t, float64(1), counterValue(t, m, lost))
		require.ErrorIs(t, s.LastStorageErr(), errStoreFailed)
	})
}

// teeInfoResponse builds a TEE_INFO result, which ProcessAndStore accepts without a
// signature before the TEE identity is set, so it reaches storage in tests.
func teeInfoResponse(id common.Hash, tag types.SubmissionTag) *types.ActionResponse {
	return &types.ActionResponse{
		Result: types.ActionResult{
			ID:            id,
			SubmissionTag: tag,
			Status:        1,
			OPCommand:     op.TEEInfo.Hash(),
			Data:          []byte("info"),
		},
	}
}

// counterValue sums the value of the named Prometheus counter family in m's registry.
func counterValue(t *testing.T, m *metrics.Metrics, name string) float64 {
	t.Helper()
	fams, err := m.Registry().Gather()
	require.NoError(t, err)
	var sum float64
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			sum += metric.GetCounter().GetValue()
		}
	}
	return sum
}

var errStoreFailed = errors.New("backend write failed")

// failingStorage reports no existing result (so the override guard is skipped) and
// fails the write, exercising ProcessAndStore's genuine lost-result path.
type failingStorage struct{}

func (failingStorage) Set(context.Context, string, *types.ActionResponse) error { return nil }

func (failingStorage) SetWithTTL(context.Context, string, *types.ActionResponse, time.Duration) error {
	return errStoreFailed
}

func (failingStorage) Get(context.Context, string) (*types.ActionResponse, error) {
	return nil, storage.ErrNotFound
}

func (failingStorage) Remove(context.Context, string) error { return nil }
