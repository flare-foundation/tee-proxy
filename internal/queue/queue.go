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

	// queueOpTimeout bounds the Redis operations of a delivery, which run detached from
	// the caller's cancellation.
	queueOpTimeout = 5 * time.Second

	// MaxDeliveryAttempts caps how many times an action is offered to the node before it
	// is given up, so an action the node can never accept cannot block its queue forever.
	MaxDeliveryAttempts = 5
)

var (
	// ErrInvalidQueueID is returned when an unrecognized queue ID is provided.
	ErrInvalidQueueID = errors.New("invalid queue id")

	// ErrDeliveryExhausted is returned by Delivery.Restore once MaxDeliveryAttempts is spent.
	ErrDeliveryExhausted = errors.New("delivery attempts exhausted")
)

// ActionSubmissionID uniquely identifies an action by its action ID and submission tag.
type ActionSubmissionID struct {
	ActionID      common.Hash
	SubmissionTag types.SubmissionTag
	// Attempt counts the deliveries already attempted; absent on entries queued before
	// redelivery existed, and never part of the action's storage key.
	Attempt uint8 `json:"attempt,omitempty"`
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
				// A scrape-time Redis failure is reported as depth 0, recorded in the
				// read-failure counter, and warn-logged so an outage is distinguishable
				// from a genuinely drained queue.
				as.metrics.QueueDepthReadFailed(label)
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

	ql := queueLabel(queueID)

	queue, err := as.queueByID(queueID)
	if err != nil {
		as.metrics.ActionEnqueued(ql, "invalid_queue")
		return err
	}

	err = as.actions.SetWithTTL(ctx, id.String(), action, as.actionTTL)
	if err != nil {
		as.metrics.ActionEnqueued(ql, "store_error")
		return fmt.Errorf("storing action: %w", err)
	}

	err = queue.Enqueue(ctx, &id)
	if err != nil {
		as.metrics.ActionEnqueued(ql, "queue_error")
		return fmt.Errorf("enqueueing to %s: %w", queueID, err)
	}

	as.metrics.ActionEnqueued(ql, "success")

	if length, lerr := queue.QueueLength(ctx); lerr == nil && length > queueDepthWarnThreshold {
		logger.Warnf("queue %s depth %d exceeds threshold %d", queueID, length, queueDepthWarnThreshold)
	}

	return nil
}

// Delivery is an action taken off its queue whose body is still stored.
// Exactly one of Commit or Restore must be called once the send outcome is known;
// calling neither leaves the action off its queue until the body expires.
type Delivery struct {
	// Action is the dequeued action.
	Action *types.Action

	id      ActionSubmissionID
	queue   storage.Queue[*ActionSubmissionID]
	queueID processorutils.QueueID
	actions storage.Storage[*types.Action]
	metrics *metrics.Metrics
}

// Commit drops the action body once the node has received the action.
func (d *Delivery) Commit() error {
	// Cancellation is deliberately not honoured: the queue ID is already consumed.
	ctx, cancel := context.WithTimeout(context.Background(), queueOpTimeout)
	defer cancel()

	if err := d.actions.Remove(ctx, d.id.String()); err != nil {
		return fmt.Errorf("removing delivered action %s: %w", d.id.String(), err)
	}

	return nil
}

// Restore returns an action the node did not receive to the dequeue end of its queue,
// so the next poll retries it. A non-nil error means the action was given up instead:
// its attempts are spent or the queue write failed, and the action is lost.
func (d *Delivery) Restore() error {
	ql := queueLabel(d.queueID)

	// Cancellation is deliberately not honoured: the queue ID is already consumed.
	ctx, cancel := context.WithTimeout(context.Background(), queueOpTimeout)
	defer cancel()

	next := d.id
	next.Attempt = d.id.Attempt + 1

	if next.Attempt >= MaxDeliveryAttempts {
		d.metrics.ActionRedelivery(ql, "exhausted")
		d.actions.Remove(ctx, d.id.String()) //nolint:errcheck // best effort; the body expires with its TTL
		logger.Errorf("action %v (tag %v, queue %v) undelivered after %d attempts, action lost",
			d.id.ActionID, d.id.SubmissionTag, d.queueID, MaxDeliveryAttempts)

		return fmt.Errorf("%w for action %s", ErrDeliveryExhausted, d.id.String())
	}

	if err := d.queue.Requeue(ctx, &next); err != nil {
		d.metrics.ActionRedelivery(ql, "requeue_failed")
		logger.Errorf("requeueing undelivered action %v (tag %v, queue %v) failed, action lost: %v",
			d.id.ActionID, d.id.SubmissionTag, d.queueID, err)

		return fmt.Errorf("requeueing action %s: %w", d.id.String(), err)
	}

	d.metrics.ActionRedelivery(ql, "requeued")
	logger.Warnf("action %v (tag %v, queue %v) undelivered, requeued after attempt %d of %d",
		d.id.ActionID, d.id.SubmissionTag, d.queueID, next.Attempt, MaxDeliveryAttempts)

	return nil
}

// Dequeue takes the next action off the indicated queue and returns it as a Delivery the
// caller must Commit or Restore. If no action is available, wrapped ErrEmptyQueue is returned.
//
// ctx cancellation is honoured only before the queue ID is consumed: an interrupted body
// fetch would leave the action neither queued nor delivered.
func (as *ActionQueues) Dequeue(ctx context.Context, queueID processorutils.QueueID) (*Delivery, error) {
	queue, err := as.queueByID(queueID)
	if err != nil {
		return nil, err
	}

	ql := queueLabel(queueID)

	if err := ctx.Err(); err != nil {
		// caller went away before the poll; no ID consumed, nothing orphaned
		as.metrics.ActionDequeued(ql, "cancelled")
		return nil, fmt.Errorf("dequeuing %v: %w", queueID, err)
	}

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queueOpTimeout)
	defer cancel()

	storingID, err := queue.Dequeue(opCtx)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrEmptyQueue):
			as.metrics.ActionDequeued(ql, "empty")
		default:
			// the pop itself failed; no ID consumed, so distinct from the orphan-signalling "error"
			as.metrics.ActionDequeued(ql, "dequeue_error")
		}
		return nil, fmt.Errorf("dequeuing %v: %w", queueID, err)
	}

	action, err := as.actions.Get(opCtx, storingID.String())
	if errors.Is(err, storage.ErrNotFound) {
		as.metrics.ActionDequeued(ql, "action_not_found")
		return nil, fmt.Errorf("queued action not found: %s", storingID.String())
	}
	if err != nil {
		as.metrics.ActionDequeued(ql, "error")
		return nil, fmt.Errorf("fetching queued action %s: %w", storingID.String(), err)
	}

	as.metrics.ActionDequeued(ql, "success")

	return &Delivery{
		Action:  action,
		id:      *storingID,
		queue:   queue,
		queueID: queueID,
		actions: as.actions,
		metrics: as.metrics,
	}, nil
}

// QueueLength returns the number of elements in the main queue.
func (as *ActionQueues) QueueLength(ctx context.Context) (int64, error) {
	return as.mainQueue.QueueLength(ctx)
}
