package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/redis/go-redis/v9"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

type QueueID string

const (
	Read QueueID = "read"
	Main QueueID = "main"
)

var ErrInvalidQueueID = fmt.Errorf("%w: invalid queue id", status.HTTP[400])

type StoringID struct {
	ActionID      common.Hash
	SubmissionTag types.SubmissionTag
}

func (id *StoringID) String() string {
	return fmt.Sprintf("%s:%s", id.ActionID, id.SubmissionTag)
}

type ActionQueues struct {
	actions   *Storage[*types.Action]
	readQueue *Storage[*StoringID]
	mainQueue *Storage[*StoringID]
}

type ResponseStorage struct {
	s *Storage[*types.ActionResponse]
}

func NewActionQueues(client *redis.Client) *ActionQueues {
	return &ActionQueues{
		actions:   NewStore[*types.Action](Actions, client),
		readQueue: NewStore[*StoringID](ReadQueue, client),
		mainQueue: NewStore[*StoringID](MainQueue, client),
	}
}

func (as *ActionQueues) Enqueue(ctx context.Context, action *types.Action, queueID QueueID) error {
	id := StoringID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}

	err := as.actions.Set(ctx, id.String(), action)
	if err != nil {
		return err
	}

	switch queueID {
	case Main:
		err = as.mainQueue.Enqueue(ctx, &id)
	case Read:
		err = as.readQueue.Enqueue(ctx, &id)
	default:
		return ErrInvalidQueueID
	}

	return err
}

var ErrEmptyQueue = fmt.Errorf("%w: empty queue", status.HTTP[404])

// Dequeue dequeues action from indicated queue. If no action is available, wrapped ErrEmptyQueue is dequeued.
func (as *ActionQueues) Dequeue(ctx context.Context, id QueueID) (*types.Action, error) {
	var queue *Storage[*StoringID]

	switch id {
	case Main:
		queue = as.mainQueue
	case Read:
		queue = as.readQueue
	default:
		return nil, ErrInvalidQueueID
	}

	storingID, err := queue.Dequeue(ctx)
	if err != nil {
		return nil, fmt.Errorf("dequeuing %v: %w", id, err)
	}

	action, err := as.actions.Get(ctx, storingID.String())
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("queued action not found: %s", storingID.String())
	}

	return action, err
}

// QueueLength returns the number of elements in the main queue.
func (as *ActionQueues) QueueLength(ctx context.Context) (int64, error) {
	return as.mainQueue.GetQueueLength(ctx)
}

func NewResultStorage(client *redis.Client) *ResponseStorage {
	return &ResponseStorage{
		NewStore[*types.ActionResponse](Results, client),
	}
}

// StoreResponse stores response with identifier actionID:submissionTag for 2 weeks.
func (rs *ResponseStorage) StoreResponse(ctx context.Context, result *types.ActionResponse) error {
	id := StoringID{ActionID: result.Result.ID, SubmissionTag: result.Result.SubmissionTag}

	err := rs.s.SetWithTTL(ctx, id.String(), result, 14*24*time.Hour)
	if err != nil {
		return fmt.Errorf("storing response %s: %v", id.String(), err)
	}

	return nil
}

// GetResponse returns action response for action id and submission tag.
func (rs *ResponseStorage) GetResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	id := StoringID{ActionID: actionID, SubmissionTag: submissionTag}

	result, err := rs.s.Get(ctx, id.String())
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: response not in storage: %w", status.HTTP[404], err)
	}

	if err != nil {
		return nil, fmt.Errorf("reading response for %s: %w", id.String(), err)
	}

	return result, nil
}

// WaitOnResponse waits on the response for the actionID with submissionTag until timeout runs out.
//
// If timeout is not positive, it waits until the response arrives.
// Should only be used if an action with such ID and submission tag has been put into action queue.
func (rs *ResponseStorage) WaitOnResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag, timeout time.Duration) (*types.ActionResponse, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var response *types.ActionResponse
	var err error

	for {
		if err = ctx.Err(); err != nil {
			return nil, fmt.Errorf("waiting for the response for %v, %v: %w", actionID, submissionTag, err)
		}

		response, err = rs.GetResponse(ctx, actionID, submissionTag) // todo retry
		if err == nil {
			return response, nil
		}

		time.Sleep(time.Second)
	}
}
