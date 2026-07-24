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
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

func TestActionQueues(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	q := NewActionQueues(c, time.Hour, nil)

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

	err := q.Enqueue(ctx, action, processorutils.Main)
	require.NoError(t, err)

	retrievedAction, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)

	require.Equal(t, *action, *retrievedAction)
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

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_action_dequeue_total" {
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
			if gotQueue == "main" && gotResult == result {
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
