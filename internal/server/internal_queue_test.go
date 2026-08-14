package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	proxytest "github.com/flare-foundation/tee-proxy/internal/testutil"
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

// queueRequest builds a POST /queue/{queueID} request bound to ctx.
func queueRequest(ctx context.Context, queueID processorutils.QueueID) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/queue/"+string(queueID), nil).WithContext(ctx)
	r.SetPathValue("queueID", string(queueID))

	return r
}

func testAction() *types.Action {
	return &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("id")),
			SubmissionTag: types.Threshold,
		},
	}
}

// TestQueueHSendFailureRequeues proves a failed write does not consume the action: it goes
// back on the queue for the next poll and is not yet counted as a finalized-action loss.
func TestQueueHSendFailureRequeues(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Voting: true, Queue: true})
	aq := queue.NewActionQueues(c, time.Hour, m)
	srv := &Internal{actionQueues: aq, metrics: m}

	action := testAction()
	require.NoError(t, aq.Enqueue(context.Background(), action, processorutils.Main))

	err := srv.queueH(&errWriter{}, queueRequest(context.Background(), processorutils.Main))
	require.ErrorContains(t, err, "action requeued")
	require.Equal(t, float64(0), lostSendFailed(t, m), "a requeued action is not lost")

	// The same action, body and all, is available to the next poll.
	d, err := aq.Dequeue(context.Background(), processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, action.Data.ID, d.Action.Data.ID)
}

// TestQueueHSendFailureExhausts proves redelivery is bounded and that only giving up counts
// as a finalized-action loss, for the main queue alone.
func TestQueueHSendFailureExhausts(t *testing.T) {
	tests := []struct {
		queue    processorutils.QueueID
		wantLost float64
	}{
		{queue: processorutils.Main, wantLost: 1},
		{queue: processorutils.Direct, wantLost: 0}, // non-main losses are logged, not counted
	}
	for _, tc := range tests {
		t.Run(string(tc.queue), func(t *testing.T) {
			mr := miniredis.RunT(t)
			c := storage.NewClient(mr.Addr())
			defer c.Close() //nolint:errcheck

			m := metrics.New(metrics.Config{Enable: true, Voting: true, Queue: true})
			aq := queue.NewActionQueues(c, time.Hour, m)
			srv := &Internal{actionQueues: aq, metrics: m}

			require.NoError(t, aq.Enqueue(context.Background(), testAction(), tc.queue))

			for attempt := 1; attempt < queue.MaxDeliveryAttempts; attempt++ {
				err := srv.queueH(&errWriter{}, queueRequest(context.Background(), tc.queue))
				require.ErrorContainsf(t, err, "action requeued", "attempt %d", attempt)
			}

			err := srv.queueH(&errWriter{}, queueRequest(context.Background(), tc.queue))
			require.ErrorContains(t, err, "action lost")
			require.Equal(t, tc.wantLost, lostSendFailed(t, m))

			_, err = aq.Dequeue(context.Background(), tc.queue)
			require.ErrorIs(t, err, storage.ErrEmptyQueue)
		})
	}
}

// TestQueueHRequeuesWhenNodeGone covers the node's poll timing out while the action is being
// dequeued: nothing is written to the dead connection and the action stays deliverable.
func TestQueueHRequeuesWhenNodeGone(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Voting: true, Queue: true})
	aq := queue.NewActionQueues(c, time.Hour, m)
	srv := &Internal{actionQueues: aq, metrics: m}

	action := testAction()
	require.NoError(t, aq.Enqueue(context.Background(), action, processorutils.Main))

	// Drop the request context once the ID is popped, as a node whose poll timed out does.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.AddHook(proxytest.AfterCommand("rpop", cancel))

	w := httptest.NewRecorder()
	err := srv.queueH(w, queueRequest(ctx, processorutils.Main))
	require.ErrorContains(t, err, "action requeued")
	require.Empty(t, w.Body.String(), "no action may be written to a connection the node closed")
	require.Equal(t, float64(0), lostSendFailed(t, m))

	d, err := aq.Dequeue(context.Background(), processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, action.Data.ID, d.Action.Data.ID)
}

// TestQueueEndpointRequeuesOnNodeTimeout drives the endpoint over a real connection with a
// client that gives up mid-dequeue, as the node's poll timeout does, and proves the action
// survives for the next poll.
func TestQueueEndpointRequeuesOnNodeTimeout(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Voting: true, Queue: true})
	aq := queue.NewActionQueues(c, time.Hour, m)
	srv := &Internal{actionQueues: aq, metrics: m}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /queue/{queueID}", prepareHandler(srv.queueH, noBody, true))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	action := testAction()
	require.NoError(t, aq.Enqueue(context.Background(), action, processorutils.Main))

	// Stall the first dequeue past the client's timeout, so its connection is gone before the write.
	var once sync.Once
	c.AddHook(proxytest.AfterCommand("rpop", func() {
		once.Do(func() { time.Sleep(300 * time.Millisecond) })
	}))

	client := http.Client{Timeout: 100 * time.Millisecond}
	resp, err := client.Post(ts.URL+"/queue/main", "", nil) //nolint:bodyclose // the request times out, so there is no body
	require.Error(t, err, "the node's poll must time out")
	require.Nil(t, resp)

	require.Eventually(t, func() bool {
		n, err := aq.QueueLength(context.Background())
		return err == nil && n == 1
	}, 2*time.Second, 10*time.Millisecond, "action was not requeued")

	d, err := aq.Dequeue(context.Background(), processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, action.Data.ID, d.Action.Data.ID)
	require.Equal(t, float64(0), lostSendFailed(t, m))
}
