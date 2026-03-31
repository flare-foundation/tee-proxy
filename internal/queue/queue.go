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
)

var ErrInvalidQueueID = errors.New("invalid queue id")

type ActionSubmissionID struct {
	ActionID      common.Hash
	SubmissionTag types.SubmissionTag
}

func (id *ActionSubmissionID) String() string {
	return fmt.Sprintf("%s:%s", id.ActionID, id.SubmissionTag)
}

type ActionQueues struct {
	actions     storage.Storage[*types.Action]
	directQueue storage.Queue[*ActionSubmissionID]
	mainQueue   storage.Queue[*ActionSubmissionID]
	backupQueue storage.Queue[*ActionSubmissionID]
}

func NewActionQueues(client *redis.Client) *ActionQueues {
	return &ActionQueues{
		actions:     storage.NewRedisStorage[*types.Action](Actions, client),
		directQueue: storage.NewQueue[*ActionSubmissionID](DirectQueue, client),
		mainQueue:   storage.NewQueue[*ActionSubmissionID](MainQueue, client),
		backupQueue: storage.NewQueue[*ActionSubmissionID](BackupQueue, client),
	}
}

func (as *ActionQueues) Enqueue(ctx context.Context, action *types.Action, queueID processorutils.QueueID) error {
	id := ActionSubmissionID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}

	logger.Debugf("enqueue action %s, type %s, tag %s, queue %s", action.Data.ID, action.Data.Type, action.Data.SubmissionTag, queueID)

	err := as.actions.SetWithTTL(ctx, id.String(), action, 30*24*time.Hour) // todo: magic constant
	if err != nil {
		return fmt.Errorf("storing action: %w", err)
	}

	switch queueID {
	case processorutils.Main:
		err = as.mainQueue.Enqueue(ctx, &id)
	case processorutils.Direct:
		err = as.directQueue.Enqueue(ctx, &id)
	case processorutils.Backup:
		err = as.backupQueue.Enqueue(ctx, &id)
	default:
		return ErrInvalidQueueID
	}

	if err != nil {
		return fmt.Errorf("enqueueing to %s: %w", queueID, err)
	}

	return nil
}

// Dequeue dequeues action from indicated queue. If no action is available, wrapped ErrEmptyQueue is dequeued.
func (as *ActionQueues) Dequeue(ctx context.Context, queueID processorutils.QueueID) (*types.Action, error) {
	var queue storage.Queue[*ActionSubmissionID]

	switch queueID {
	case processorutils.Main:
		queue = as.mainQueue
	case processorutils.Direct:
		queue = as.directQueue
	case processorutils.Backup:
		queue = as.backupQueue
	default:
		return nil, ErrInvalidQueueID
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
