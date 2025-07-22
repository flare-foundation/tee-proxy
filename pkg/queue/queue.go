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

	// inProcessingQueue *Storage[*types.Action] // If Redis.Nil not in processing
}

type ResponseStorage struct {
	s *Storage[*types.ActionResponse]
}

func NewActionQueues(client *redis.Client) *ActionQueues {
	return &ActionQueues{
		actions:   NewStore[*types.Action](Actions, client),
		readQueue: NewStore[*StoringID](ReadQueue, client),
		mainQueue: NewStore[*StoringID](MainQueue, client),
		// inProcessingQueue: NewStore[*types.Action](InProcessingQueue, client),
	}
}

// ! 1. EnqueueQueuedAction

// ! 2. PopQueuedAction:

// & 3. Send the action to the TEE
// & 4. Receive the action result from the TEE
// & 5. Process the result (update the wallets or the policy)

// ! 6. StoreQueuedActionResult

// ! 8. ReadQueuedActionResult

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

func (as *ActionQueues) GetAction(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.Action, error) {
	id := StoringID{ActionID: actionID, SubmissionTag: submissionTag}

	action, err := as.actions.Get(ctx, id.String())
	if err != nil {
		return nil, fmt.Errorf("error for action ID %v, %v: %v ", actionID, submissionTag, err)
	}

	return action, nil
}

var ErrEmptyQueue = fmt.Errorf("%w: empty queue", status.HTTP[404])

func (as *ActionQueues) Pop(ctx context.Context, id QueueID) (*types.Action, error) {
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
	if errors.Is(err, redis.Nil) {
		return nil, ErrEmptyQueue
	}
	if err != nil {
		return nil, fmt.Errorf("dequeuing %v: %v", id, err)
	}

	action, err := as.actions.Get(ctx, storingID.String())
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("queued action not found: %s", storingID.String())
	}

	// err = ds.inProcessingQueue.Set(ctx, directionID.String(), direction)
	// if err != nil {
	// 	return nil, err
	// }

	return action, nil
}

// GetQueuedActionCount returns the number of elements in the actionStorage.
func (as *ActionQueues) QueueLength(ctx context.Context) (int64, error) {
	return as.mainQueue.GetQueueLength(ctx)
}

func NewResponseStorage(client *redis.Client) *ResponseStorage {
	return &ResponseStorage{
		NewStore[*types.ActionResponse](Results, client),
	}
}

// StoreResponse stores response to storage.
func (rs *ResponseStorage) StoreResponse(ctx context.Context, response *types.ActionResponse) error {
	id := StoringID{ActionID: response.Result.ID, SubmissionTag: response.Result.SubmissionTag}

	err := rs.s.Set(ctx, id.String(), response)
	if err != nil {
		return fmt.Errorf("storing result %s: %v", id.String(), err)
	}

	return nil
}

// GetResponse returns response to action with ID and submission tag if it is in the storage.
func (rs *ResponseStorage) GetResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	id := StoringID{ActionID: actionID, SubmissionTag: submissionTag}

	result, err := rs.s.Get(ctx, id.String())
	if err != nil {
		return nil, fmt.Errorf("reading result for %s: %v", id.String(), err)
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
			return nil, err
		}

		response, err = rs.GetResponse(ctx, actionID, submissionTag) // todo retry
		if err != nil {
			return response, nil
		}

		time.Sleep(time.Second)
	}
}
