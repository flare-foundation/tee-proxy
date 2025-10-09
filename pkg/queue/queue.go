package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/redis/go-redis/v9"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

const (
	Actions = "Action"
	Results = "Results"

	DirectQueue = "DirectQueue"
	MainQueue   = "MainQueue"
)

var ErrInvalidQueueID = errors.New("invalid queue id")

type StoringID struct {
	ActionID      common.Hash
	SubmissionTag types.SubmissionTag
}

func (id *StoringID) String() string {
	return fmt.Sprintf("%s:%s", id.ActionID, id.SubmissionTag)
}

type ActionQueues struct {
	actions     *storage.Storage[*types.Action]
	directQueue *storage.Storage[*StoringID]
	mainQueue   *storage.Storage[*StoringID]
}

type ResponseStorage struct {
	s *storage.Storage[*types.ActionResponse]
}

func NewActionQueues(client *redis.Client) *ActionQueues {
	return &ActionQueues{
		actions:     storage.New[*types.Action](Actions, client),
		directQueue: storage.New[*StoringID](DirectQueue, client),
		mainQueue:   storage.New[*StoringID](MainQueue, client),
	}
}

func (as *ActionQueues) Enqueue(ctx context.Context, action *types.Action, queueID processorutils.QueueID) error {
	id := StoringID{ActionID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag}

	err := as.actions.SetWithTTL(ctx, id.String(), action, 30*24*time.Hour)
	if err != nil {
		return err
	}

	switch queueID {
	case processorutils.Main:
		err = as.mainQueue.Enqueue(ctx, &id)
	case processorutils.Direct:
		err = as.directQueue.Enqueue(ctx, &id)
	default:
		return ErrInvalidQueueID
	}

	return err
}

// Dequeue dequeues action from indicated queue. If no action is available, wrapped ErrEmptyQueue is dequeued.
func (as *ActionQueues) Dequeue(ctx context.Context, id processorutils.QueueID) (*types.Action, error) {
	var queue *storage.Storage[*StoringID]

	switch id {
	case processorutils.Main:
		queue = as.mainQueue
	case processorutils.Direct:
		queue = as.directQueue
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

	as.actions.Remove(ctx, storingID.String()) //nolint:errcheck // error can only happen if context is canceled

	return action, err
}

// QueueLength returns the number of elements in the main queue.
func (as *ActionQueues) QueueLength(ctx context.Context) (int64, error) {
	return as.mainQueue.QueueLength(ctx)
}

func NewResultStorage(client *redis.Client) *ResponseStorage {
	return &ResponseStorage{
		storage.New[*types.ActionResponse](Results, client),
	}
}

// StoreResponse stores response with identifier actionID:submissionTag for 2 weeks for end and threshold actions, or half an hour for submit actions.
func (rs *ResponseStorage) StoreResponse(ctx context.Context, response *types.ActionResponse) error {
	id := StoringID{ActionID: response.Result.ID, SubmissionTag: response.Result.SubmissionTag}

	storingDuration := 14 * 24 * time.Hour
	if response.Result.SubmissionTag == types.Submit {
		storingDuration = 30 * time.Minute
	}

	// do not override final result with an intermediate result
	if response.Result.Status >= 2 {
		res, err := rs.s.Get(ctx, id.String())
		if err == nil && res.Result.Status < 2 {
			return nil
		}
	}

	err := rs.s.SetWithTTL(ctx, id.String(), response, storingDuration)
	if err != nil {
		return fmt.Errorf("storing response %s: %v", id.String(), err)
	}

	return nil
}

// GetResponse returns action response for action id and submission tag.
func (rs *ResponseStorage) GetResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	id := StoringID{ActionID: actionID, SubmissionTag: submissionTag}

	response, err := rs.s.Get(ctx, id.String())
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: response not in storage: %w", status.HTTP[404], err)
	}

	if err != nil {
		return nil, fmt.Errorf("reading response for %s: %w", id.String(), err)
	}

	return response, nil
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
