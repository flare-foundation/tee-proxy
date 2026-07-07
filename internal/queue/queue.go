package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/redis/go-redis/v9"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

const (
	Actions = "Action"

	DirectQueue = "DirectQueue"
	MainQueue   = "MainQueue"
	BackupQueue = "BackupQueue"

	// queueDepthWarnThreshold logs a warning past this depth; not a hard cap.
	queueDepthWarnThreshold = 100

	// queueDepthTimeout bounds the Redis LLEN issued when the depth gauge is scraped.
	queueDepthTimeout = 2 * time.Second
)

// ErrInvalidQueueID is returned when an unrecognized queue ID is provided.
var ErrInvalidQueueID = errors.New("invalid queue id")

// ActionSubmissionID uniquely identifies an action by its action ID and submission tag.
type ActionSubmissionID struct {
	ActionID      common.Hash
	SubmissionTag types.SubmissionTag
}

// String returns a combined string representation of the action ID and submission tag.
func (id *ActionSubmissionID) String() string {
	return fmt.Sprintf("%s:%s", id.ActionID, id.SubmissionTag)
}

// ActionQueues manages direct, main, and backup Redis-backed queues for action submission.
type ActionQueues struct {
	actions     storage.Storage[*types.Action]
	directQueue storage.Queue[*ActionSubmissionID]
	mainQueue   storage.Queue[*ActionSubmissionID]
	backupQueue storage.Queue[*ActionSubmissionID]
	actionTTL   time.Duration

	metrics *metrics.Metrics
}

// NewActionQueues creates a new ActionQueues backed by the given Redis client.
// m may be nil or disabled.
func NewActionQueues(client *redis.Client, actionTTL time.Duration, m *metrics.Metrics) *ActionQueues {
	as := &ActionQueues{
		actions:     storage.NewRedisStorage[*types.Action](Actions, client),
		directQueue: storage.NewQueue[*ActionSubmissionID](DirectQueue, client),
		mainQueue:   storage.NewQueue[*ActionSubmissionID](MainQueue, client),
		backupQueue: storage.NewQueue[*ActionSubmissionID](BackupQueue, client),
		actionTTL:   actionTTL,
		metrics:     m,
	}

	as.registerDepthGauges()

	return as
}

// registerDepthGauges registers one scrape-time depth gauge per queue. No-op when
// queue metrics are disabled.
func (as *ActionQueues) registerDepthGauges() {
	for _, qq := range []struct {
		label string
		queue storage.Queue[*ActionSubmissionID]
	}{
		{"direct", as.directQueue},
		{"main", as.mainQueue},
		{"backup", as.backupQueue},
	} {
		q, label := qq.queue, qq.label
		as.metrics.RegisterQueueDepth(label, func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), queueDepthTimeout)
			defer cancel()
			n, err := q.QueueLength(ctx)
			if err != nil {
				// A scrape-time Redis failure is reported as depth 0; warn so an
				// outage is distinguishable from a genuinely drained queue.
				logger.Warnf("queue depth gauge: reading %s queue length: %v", label, err)
				return 0
			}
			return float64(n)
		})
	}
}

// queueLabel maps a queue ID to a bounded metric label.
func queueLabel(queueID processorutils.QueueID) string {
	switch queueID {
	case processorutils.Main:
		return "main"
	case processorutils.Direct:
		return "direct"
	case processorutils.Backup:
		return "backup"
	default:
		return "other"
	}
}

func (as *ActionQueues) queueByID(queueID processorutils.QueueID) (storage.Queue[*ActionSubmissionID], error) {
	switch queueID {
	case processorutils.Main:
		return as.mainQueue, nil
	case processorutils.Direct:
		return as.directQueue, nil
	case processorutils.Backup:
		return as.backupQueue, nil
	default:
		return nil, ErrInvalidQueueID
	}
}

// Enqueue stores the action and appends its submission ID to the indicated queue.
func (as *ActionQueues) Enqueue(ctx context.Context, action *types.Action, queueID processorutils.QueueID) error {
	id := ActionSubmissionID{
		ActionID:      action.Data.ID,
		SubmissionTag: action.Data.SubmissionTag,
	}

	logger.Debugf("enqueue action %s, type %s, tag %s, queue %s", action.Data.ID, action.Data.Type, action.Data.SubmissionTag, queueID)

	queue, err := as.queueByID(queueID)
	if err != nil {
		return err
	}

	err = as.actions.SetWithTTL(ctx, id.String(), action, as.actionTTL)
	if err != nil {
		return fmt.Errorf("storing action: %w", err)
	}

	err = queue.Enqueue(ctx, &id)
	if err != nil {
		return fmt.Errorf("enqueueing to %s: %w", queueID, err)
	}

	if length, lerr := queue.QueueLength(ctx); lerr == nil && length > queueDepthWarnThreshold {
		logger.Warnf("queue %s depth %d exceeds threshold %d", queueID, length, queueDepthWarnThreshold)
	}

	return nil
}

// Dequeue dequeues action from indicated queue. If no action is available, wrapped ErrEmptyQueue is dequeued.
func (as *ActionQueues) Dequeue(ctx context.Context, queueID processorutils.QueueID) (*types.Action, error) {
	queue, err := as.queueByID(queueID)
	if err != nil {
		return nil, err
	}

	ql := queueLabel(queueID)

	storingID, err := queue.Dequeue(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrEmptyQueue) {
			as.metrics.ActionDequeued(ql, "empty")
		} else {
			as.metrics.ActionDequeued(ql, "error")
		}
		return nil, fmt.Errorf("dequeuing %v: %w", queueID, err)
	}

	action, err := as.actions.Get(ctx, storingID.String())
	if errors.Is(err, storage.ErrNotFound) {
		as.metrics.ActionDequeued(ql, "action_not_found")
		return nil, fmt.Errorf("queued action not found: %s", storingID.String())
	}
	if err != nil {
		as.metrics.ActionDequeued(ql, "error")
		return nil, fmt.Errorf("fetching queued action %s: %w", storingID.String(), err)
	}

	as.actions.Remove(ctx, storingID.String()) //nolint:errcheck // error can only happen if context is canceled

	as.metrics.ActionDequeued(ql, "success")

	return action, nil
}

// QueueLength returns the number of elements in the main queue.
func (as *ActionQueues) QueueLength(ctx context.Context) (int64, error) {
	return as.mainQueue.QueueLength(ctx)
}
