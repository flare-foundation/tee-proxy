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
# HELP teeproxy_action_dequeue_total Action dequeue attempts by queue and result; a success result returned a body, any other result dequeued nothing.
# TYPE teeproxy_action_dequeue_total counter
teeproxy_action_dequeue_total{queue="main",result="empty"} 1
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
# HELP teeproxy_action_dequeue_total Action dequeue attempts by queue and result; a success result returned a body, any other result dequeued nothing.
# TYPE teeproxy_action_dequeue_total counter
teeproxy_action_dequeue_total{queue="main",result="action_not_found"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_action_dequeue_total"))
}
