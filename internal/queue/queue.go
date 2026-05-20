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
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

const (
	Actions = "Action"

	DirectQueue = "DirectQueue"
	MainQueue   = "MainQueue"
	BackupQueue = "BackupQueue"

	// queueDepthWarnThreshold logs a warning past this depth; not a hard cap.
	queueDepthWarnThreshold = 100
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
}

// NewActionQueues creates a new ActionQueues backed by the given Redis client.
func NewActionQueues(client *redis.Client, actionTTL time.Duration) *ActionQueues {
	return &ActionQueues{
		actions:     storage.NewRedisStorage[*types.Action](Actions, client),
		directQueue: storage.NewQueue[*ActionSubmissionID](DirectQueue, client),
		mainQueue:   storage.NewQueue[*ActionSubmissionID](MainQueue, client),
		backupQueue: storage.NewQueue[*ActionSubmissionID](BackupQueue, client),
		actionTTL:   actionTTL,
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

	storingID, err := queue.Dequeue(ctx)
	if err != nil {
		return nil, fmt.Errorf("dequeuing %v: %w", queueID, err)
	}

	action, err := as.actions.Get(ctx, storingID.String())
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("queued action not found: %s", storingID.String())
	}

	as.actions.Remove(ctx, storingID.String()) //nolint:errcheck // error can only happen if context is canceled

	return action, err
}

// QueueLength returns the number of elements in the main queue.
func (as *ActionQueues) QueueLength(ctx context.Context) (int64, error) {
	return as.mainQueue.QueueLength(ctx)
}
