package result

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"github.com/redis/go-redis/v9"
)

const (
	defaultStoringDuration = 14 * 24 * time.Hour
	submitStoringDuration  = 30 * time.Minute
)

const (
	Results = "Results"
)

// ResultStorage provides methods for storing and retrieving action responses.
type ResultStorage struct {
	mu sync.Mutex
	s  *storage.Storage[*types.ActionResponse]
}

// NewStorage creates a new ResultStorage via a provided Redis client.
func NewStorage(client *redis.Client) *ResultStorage {
	return &ResultStorage{
		s: storage.New[*types.ActionResponse](Results, client),
	}
}

// StoreResponse stores response with identifier actionID:submissionTag for 2 weeks for end and threshold actions, or half an hour for submit actions.
func (rs *ResultStorage) StoreResponse(ctx context.Context, response *types.ActionResponse) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	id := queue.ActionSubmissionID{
		ActionID:      response.Result.ID,
		SubmissionTag: response.Result.SubmissionTag,
	}

	storingDur := defaultStoringDuration
	if response.Result.SubmissionTag == types.Submit {
		storingDur = submitStoringDuration
	}

	// do not override final results (0 or 1) or if new status is smaller than other
	res, _ := rs.s.Get(ctx, id.String()) // error means that the id is not stored, any other type of error will be caught by next call to Redis
	if res != nil {
		if res.Result.Status < 2 {
			return fmt.Errorf("tried to override final status for %s", id.String())
		}
		if response.Result.Status >= 2 && res.Result.Status >= response.Result.Status {
			return fmt.Errorf("trying to override higher transient status for %s", id.String())
		}
	}

	err := rs.s.SetWithTTL(ctx, id.String(), response, storingDur)
	if err != nil {
		return fmt.Errorf("storing response %s: %w", id.String(), err)
	}

	rs.s.Publish(ctx, id.String()) //nolint:errcheck

	return nil
}

// GetResponse returns action response for action id and submission tag.
func (rs *ResultStorage) GetResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	id := queue.ActionSubmissionID{ActionID: actionID, SubmissionTag: submissionTag}

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
// Should only be used if an action with the given ID and submission tag is expected to be processed.
func (rs *ResultStorage) WaitOnResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag, timeout time.Duration) (*types.ActionResponse, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	id := queue.ActionSubmissionID{
		ActionID:      actionID,
		SubmissionTag: submissionTag,
	}
	pubsub := rs.s.Subscribe(ctx, id.String())
	defer pubsub.Close() //nolint:errcheck

	// Check if it was already stored.
	response, err := rs.GetResponse(ctx, actionID, submissionTag)
	if err == nil && response != nil {
		return response, nil
	}

	// Wait for a message on the channel.
	_, err = pubsub.ReceiveMessage(ctx)
	if err != nil {
		// Hopeful attempt
		finalResponse, finalErr := rs.GetResponse(ctx, actionID, submissionTag)
		if finalErr == nil {
			return finalResponse, nil
		}
		return nil, fmt.Errorf("waiting for the response for %v, %v: %w", actionID, submissionTag, err)
	}

	return rs.GetResponse(ctx, actionID, submissionTag)
}
