package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

// errWriter fails every body write, simulating a broken connection to the node.
type errWriter struct {
	header http.Header
}

func (w *errWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *errWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func (w *errWriter) WriteHeader(int) {}

// lostSendFailed returns the value of finalized_action_lost_total{reason="send_failed"}.
func lostSendFailed(t *testing.T, m *metrics.Metrics) float64 {
	t.Helper()

	mfs, err := m.Registry().Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "teeproxy_finalized_action_lost_total" {
			continue
		}
		for _, mtr := range mf.GetMetric() {
			for _, lp := range mtr.GetLabel() {
				if lp.GetName() == "reason" && lp.GetValue() == "send_failed" {
					return mtr.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatal("send_failed series not found")
	return 0
}

// TestQueueHSendFailed proves a failed write of a dequeued action returns an error
// and increments send_failed for the main queue only.
func TestQueueHSendFailed(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Voting: true})
	srv := &Internal{actionQueues: queue.NewActionQueues(c, time.Hour, nil), metrics: m}

	action := &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("id")),
			SubmissionTag: types.Threshold,
		},
	}

	tests := []struct {
		queue processorutils.QueueID
		want  float64
	}{
		{queue: processorutils.Main, want: 1},
		{queue: processorutils.Direct, want: 1}, // non-main losses are logged, not counted
	}
	for _, tc := range tests {
		require.NoError(t, srv.actionQueues.Enqueue(context.Background(), action, tc.queue))

		r := httptest.NewRequest(http.MethodPost, "/queue/"+string(tc.queue), nil)
		r.SetPathValue("queueID", string(tc.queue))

		err := srv.queueH(&errWriter{}, r)
		require.ErrorContains(t, err, "action lost")
		require.Equal(t, tc.want, lostSendFailed(t, m), "queue %s", tc.queue)
	}
}
