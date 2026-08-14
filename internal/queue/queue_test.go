package queue

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	proxytest "github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

func TestActionQueues(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	q := NewActionQueues(c, time.Hour, nil)

	ctx := context.Background()

	action := testAction("id")

	err := q.Enqueue(ctx, action, processorutils.Main)
	require.NoError(t, err)

	d, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)

	require.Equal(t, *action, *d.Action)

	// The body outlives the dequeue so an undelivered action can be requeued.
	id := ActionSubmissionID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}
	key := "Action-" + id.String()
	require.True(t, mr.Exists(key))

	require.NoError(t, d.Commit())
	require.False(t, mr.Exists(key))
}

// testAction returns a minimal action identified by name.
func testAction(name string) *types.Action {
	return &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte(name)),
			Type:          types.Direct,
			SubmissionTag: types.Threshold,
			Message:       hexutil.Bytes{},
		},
		AdditionalVariableMessages: []hexutil.Bytes{},
		Timestamps:                 []uint64{},
		AdditionalActionData:       hexutil.Bytes{},
		Signatures:                 []hexutil.Bytes{},
	}
}

// redeliveryCount reads teeproxy_action_redelivery_total for the main queue and given result.
func redeliveryCount(t *testing.T, m *metrics.Metrics, result string) float64 {
	t.Helper()

	return counterValue(t, m, "teeproxy_action_redelivery_total", "main", result)
}

// TestRestoreRequeuesForNextPoll proves an undelivered action goes back to the dequeue end
// of its queue with its body intact, so the next poll delivers the very same action.
func TestRestoreRequeuesForNextPoll(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	first, second := testAction("first"), testAction("second")
	require.NoError(t, q.Enqueue(ctx, first, processorutils.Main))
	require.NoError(t, q.Enqueue(ctx, second, processorutils.Main))

	d, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, first.Data.ID, d.Action.Data.ID)

	require.NoError(t, d.Restore())
	require.Equal(t, float64(1), redeliveryCount(t, m, "requeued"))

	// Requeued at the dequeue end, so ordering is preserved: first again, then second.
	again, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, first.Data.ID, again.Action.Data.ID)
	require.Equal(t, *first, *again.Action)

	require.NoError(t, again.Commit())

	last, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, second.Data.ID, last.Action.Data.ID)
}

// TestRestoreGivesUpAfterMaxAttempts proves redelivery is bounded: an action the node never
// accepts is dropped after MaxDeliveryAttempts instead of blocking its queue forever.
func TestRestoreGivesUpAfterMaxAttempts(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	action := testAction("undeliverable")
	require.NoError(t, q.Enqueue(ctx, action, processorutils.Main))

	for attempt := 1; attempt < MaxDeliveryAttempts; attempt++ {
		d, err := q.Dequeue(ctx, processorutils.Main)
		require.NoErrorf(t, err, "attempt %d", attempt)
		require.NoErrorf(t, d.Restore(), "attempt %d", attempt)
	}

	d, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)
	require.ErrorIs(t, d.Restore(), ErrDeliveryExhausted)

	require.Equal(t, float64(MaxDeliveryAttempts-1), redeliveryCount(t, m, "requeued"))
	require.Equal(t, float64(1), redeliveryCount(t, m, "exhausted"))

	// Nothing left behind: neither the queue ID nor the body.
	_, err = q.Dequeue(ctx, processorutils.Main)
	require.ErrorIs(t, err, storage.ErrEmptyQueue)

	id := ActionSubmissionID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}
	require.False(t, mr.Exists("Action-"+id.String()))
}

// TestRestoreFailureIsLost pins the last loss path: if the queue write fails the action is
// gone, and the caller must be told so it can be counted as a loss.
func TestRestoreFailureIsLost(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, testAction("lost"), processorutils.Main))

	d, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)

	mr.Close() // sever the backing store so the requeue fails

	require.Error(t, d.Restore())
	require.Equal(t, float64(1), redeliveryCount(t, m, "requeue_failed"))
	require.Equal(t, float64(0), redeliveryCount(t, m, "requeued"))
}

// TestDequeueBodyFetchSurvivesCancel proves the body fetch is detached from the caller: a
// poll abandoned after the pop must still yield a Delivery, never strand the popped ID.
func TestDequeueBodyFetchSurvivesCancel(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	q := NewActionQueues(c, time.Hour, nil)

	action := testAction("cancelled midway")
	require.NoError(t, q.Enqueue(context.Background(), action, processorutils.Main))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.AddHook(proxytest.AfterCommand("rpop", cancel))

	d, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)
	require.Equal(t, action.Data.ID, d.Action.Data.ID)
	require.Error(t, ctx.Err())
}

// TestActionQueuesRecordsMetrics verifies the dequeue counter and depth gauge are
// wired through the real ActionQueues over miniredis.
func TestActionQueuesRecordsMetrics(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	_, err := q.Dequeue(ctx, processorutils.Main) // empty queue
	require.ErrorIs(t, err, storage.ErrEmptyQueue)

	action := &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("id")),
			Type:          types.Direct,
			SubmissionTag: types.Threshold,
			Message:       hexutil.Bytes{},
		},
		AdditionalVariableMessages: []hexutil.Bytes{},
		Timestamps:                 []uint64{},
		AdditionalActionData:       hexutil.Bytes{},
		Signatures:                 []hexutil.Bytes{},
	}
	require.NoError(t, q.Enqueue(ctx, action, processorutils.Main))

	_, err = q.Dequeue(ctx, processorutils.Main) // success
	require.NoError(t, err)

	const expected = `
# HELP teeproxy_action_dequeue_total Action dequeue attempts by queue and result: success returned a body; empty found nothing queued; dequeue_error failed the pop itself and cancelled is a caller-side cancellation during it (no queue ID consumed, nothing lost); action_not_found and error consumed a queue ID whose body could not be fetched (an orphaned/lost action).
# TYPE teeproxy_action_dequeue_total counter
teeproxy_action_dequeue_total{queue="backup",result="action_not_found"} 0
teeproxy_action_dequeue_total{queue="backup",result="dequeue_error"} 0
teeproxy_action_dequeue_total{queue="backup",result="error"} 0
teeproxy_action_dequeue_total{queue="direct",result="action_not_found"} 0
teeproxy_action_dequeue_total{queue="direct",result="dequeue_error"} 0
teeproxy_action_dequeue_total{queue="direct",result="error"} 0
teeproxy_action_dequeue_total{queue="main",result="action_not_found"} 0
teeproxy_action_dequeue_total{queue="main",result="dequeue_error"} 0
teeproxy_action_dequeue_total{queue="main",result="empty"} 1
teeproxy_action_dequeue_total{queue="main",result="error"} 0
teeproxy_action_dequeue_total{queue="main",result="success"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_action_dequeue_total"))
}

// TestDequeueMissingAction covers the case where an ID is on the queue list
// but its action body has been evicted (e.g. TTL expiry). Dequeue must return
// a descriptive error rather than silently returning a zero action.
func TestDequeueMissingAction(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	action := &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("evicted")),
			Type:          types.Direct,
			SubmissionTag: types.Threshold,
			Message:       hexutil.Bytes{},
		},
		AdditionalVariableMessages: []hexutil.Bytes{},
		Timestamps:                 []uint64{},
		AdditionalActionData:       hexutil.Bytes{},
		Signatures:                 []hexutil.Bytes{},
	}

	require.NoError(t, q.Enqueue(ctx, action, processorutils.Main))

	// Simulate TTL expiry of the action body while its ID remains on the queue list.
	id := ActionSubmissionID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}
	mr.Del("Action-" + id.String())

	got, err := q.Dequeue(ctx, processorutils.Main)
	require.Nil(t, got)
	require.ErrorContains(t, err, "queued action not found")

	// The evicted-body path must be labeled action_not_found — distinct from a healthy
	// dequeue or a Redis error — so an operator can alert on body/ID divergence.
	const expected = `
# HELP teeproxy_action_dequeue_total Action dequeue attempts by queue and result: success returned a body; empty found nothing queued; dequeue_error failed the pop itself and cancelled is a caller-side cancellation during it (no queue ID consumed, nothing lost); action_not_found and error consumed a queue ID whose body could not be fetched (an orphaned/lost action).
# TYPE teeproxy_action_dequeue_total counter
teeproxy_action_dequeue_total{queue="backup",result="action_not_found"} 0
teeproxy_action_dequeue_total{queue="backup",result="dequeue_error"} 0
teeproxy_action_dequeue_total{queue="backup",result="error"} 0
teeproxy_action_dequeue_total{queue="direct",result="action_not_found"} 0
teeproxy_action_dequeue_total{queue="direct",result="dequeue_error"} 0
teeproxy_action_dequeue_total{queue="direct",result="error"} 0
teeproxy_action_dequeue_total{queue="main",result="action_not_found"} 1
teeproxy_action_dequeue_total{queue="main",result="dequeue_error"} 0
teeproxy_action_dequeue_total{queue="main",result="error"} 0
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_action_dequeue_total"))
}

// dequeueCount reads teeproxy_action_dequeue_total for the main queue and given result
// label from m's registry, returning 0 if no such series exists.
func dequeueCount(t *testing.T, m *metrics.Metrics, result string) float64 {
	t.Helper()

	return counterValue(t, m, "teeproxy_action_dequeue_total", "main", result)
}

// counterValue reads the named queue/result counter from m's registry, returning 0 if no
// such series exists.
func counterValue(t *testing.T, m *metrics.Metrics, name, queue, result string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, mc := range f.GetMetric() {
			var gotQueue, gotResult string
			for _, l := range mc.GetLabel() {
				switch l.GetName() {
				case "queue":
					gotQueue = l.GetValue()
				case "result":
					gotResult = l.GetValue()
				}
			}
			if gotQueue == queue && gotResult == result {
				return mc.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// TestDequeuePopFailureIsNotOrphan guards the pop-failure classification: a Redis error on
// the pop itself consumed no queue ID, so it must be labeled dequeue_error — never the
// orphan-signalling error the TeeProxyActionOrphaned alerts page on.
func TestDequeuePopFailureIsNotOrphan(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)

	mr.Close()

	_, err := q.Dequeue(context.Background(), processorutils.Main)
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrEmptyQueue)

	require.Equal(t, float64(1), dequeueCount(t, m, "dequeue_error"))
	require.Equal(t, float64(0), dequeueCount(t, m, "error"))
	require.Equal(t, float64(0), dequeueCount(t, m, "cancelled"))
}

// TestDequeueCorruptBodyIsOrphan pins the orphan boundary from the consumed side: a popped
// ID whose body exists but cannot be decoded must be labeled error (a real orphan the
// TeeProxyActionOrphaned alerts page on), never dequeue_error.
func TestDequeueCorruptBodyIsOrphan(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	action := &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("corrupt")),
			Type:          types.Direct,
			SubmissionTag: types.Threshold,
			Message:       hexutil.Bytes{},
		},
		AdditionalVariableMessages: []hexutil.Bytes{},
		Timestamps:                 []uint64{},
		AdditionalActionData:       hexutil.Bytes{},
		Signatures:                 []hexutil.Bytes{},
	}
	require.NoError(t, q.Enqueue(ctx, action, processorutils.Main))

	// Corrupt the stored body while its ID remains on the queue list.
	id := ActionSubmissionID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}
	require.NoError(t, mr.Set("Action-"+id.String(), "not json"))

	got, err := q.Dequeue(ctx, processorutils.Main)
	require.Nil(t, got)
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrEmptyQueue)

	require.Equal(t, float64(1), dequeueCount(t, m, "error"))
	require.Equal(t, float64(0), dequeueCount(t, m, "dequeue_error"))
	require.Equal(t, float64(0), dequeueCount(t, m, "action_not_found"))
}

// TestDequeueCancelledPopIsNotError guards the caller-cancellation classification: a pop
// aborted by the caller's context must be labeled cancelled, not dequeue_error or error.
func TestDequeueCancelledPopIsNotError(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := q.Dequeue(ctx, processorutils.Main)
	require.Error(t, err)

	require.Equal(t, float64(1), dequeueCount(t, m, "cancelled"))
	require.Equal(t, float64(0), dequeueCount(t, m, "dequeue_error"))
	require.Equal(t, float64(0), dequeueCount(t, m, "error"))
}

// TestEnqueueRecordsMetrics verifies teeproxy_action_enqueue_total classifies the
// success, invalid-queue, and store-error exit paths of Enqueue.
func TestEnqueueRecordsMetrics(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer c.Close() //nolint:errcheck

	m := metrics.New(metrics.Config{Enable: true, Queue: true})
	q := NewActionQueues(c, time.Hour, m)
	ctx := context.Background()

	action := &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("id")),
			Type:          types.Direct,
			SubmissionTag: types.Threshold,
			Message:       hexutil.Bytes{},
		},
		AdditionalVariableMessages: []hexutil.Bytes{},
		Timestamps:                 []uint64{},
		AdditionalActionData:       hexutil.Bytes{},
		Signatures:                 []hexutil.Bytes{},
	}

	require.NoError(t, q.Enqueue(ctx, action, processorutils.Main)) // success

	err := q.Enqueue(ctx, action, processorutils.QueueID("bogus")) // invalid queue
	require.ErrorIs(t, err, ErrInvalidQueueID)

	mr.Close() // sever the backing store so the next SetWithTTL fails

	err = q.Enqueue(ctx, action, processorutils.Main) // store error
	require.ErrorContains(t, err, "storing action")

	const expected = `
# HELP teeproxy_action_enqueue_total Action enqueue attempts by queue and result.
# TYPE teeproxy_action_enqueue_total counter
teeproxy_action_enqueue_total{queue="backup",result="queue_error"} 0
teeproxy_action_enqueue_total{queue="backup",result="store_error"} 0
teeproxy_action_enqueue_total{queue="direct",result="queue_error"} 0
teeproxy_action_enqueue_total{queue="direct",result="store_error"} 0
teeproxy_action_enqueue_total{queue="main",result="queue_error"} 0
teeproxy_action_enqueue_total{queue="main",result="store_error"} 1
teeproxy_action_enqueue_total{queue="main",result="success"} 1
teeproxy_action_enqueue_total{queue="other",result="invalid_queue"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_action_enqueue_total"))
}
