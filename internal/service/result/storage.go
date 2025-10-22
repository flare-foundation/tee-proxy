package result

import (
	"context"
	"errors"
	"fmt"
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

type ResultStorage struct {
	s *storage.Storage[*types.ActionResponse]
}

func NewStorage(client *redis.Client) *ResultStorage {
	return &ResultStorage{
		storage.New[*types.ActionResponse](Results, client),
	}
}

// StoreResponse stores response with identifier actionID:submissionTag for 2 weeks for end and threshold actions, or half an hour for submit actions.
func (rs *ResultStorage) StoreResponse(ctx context.Context, response *types.ActionResponse) error {
	id := queue.ActionSubmissionID{
		ActionID:      response.Result.ID,
		SubmissionTag: response.Result.SubmissionTag,
	}

	storingDuration := defaultStoringDuration
	if response.Result.SubmissionTag == types.Submit {
		storingDuration = submitStoringDuration
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
// Should only be used if an action with such ID and submission tag has been put into action queue.
func (rs *ResultStorage) WaitOnResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag, timeout time.Duration) (*types.ActionResponse, error) {
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

		response, err = rs.GetResponse(ctx, actionID, submissionTag)
		if err == nil {
			return response, nil
		}

		time.Sleep(time.Second)
	}
}
