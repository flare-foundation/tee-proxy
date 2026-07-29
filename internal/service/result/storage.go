package result

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

// errOverrideFinal and errOverrideTransient mark deliberate override-guard
// rejections: the incoming result is dropped to protect an already-persisted
// canonical result, so it is neither data loss nor a storage failure. Callers
// match on them (errors.Is) to keep these out of the lost-result and liveness signals.
var (
	errOverrideFinal     = errors.New("tried to override final status")
	errOverrideTransient = errors.New("trying to override higher transient status")
)

// ResultStorage provides methods for storing and retrieving action responses.
//
// mu serialises StoreResponse so the override-guard read-then-write stays atomic; reads do not take it.
type ResultStorage struct {
	mu              sync.Mutex
	s               storage.Storage[*types.ActionResponse]
	n               storage.Notifier
	resultTTL       time.Duration
	submitResultTTL time.Duration
}

// NewStorage builds a ResultStorage. resultTTL applies to Threshold/End results,
// submitResultTTL to Submit results.
func NewStorage(s storage.Storage[*types.ActionResponse], n storage.Notifier, resultTTL, submitResultTTL time.Duration) *ResultStorage {
	return &ResultStorage{
		s:               s,
		n:               n,
		resultTTL:       resultTTL,
		submitResultTTL: submitResultTTL,
	}
}

// StoreResponse stores response under actionID:submissionTag with the appropriate TTL.
func (rs *ResultStorage) StoreResponse(ctx context.Context, response *types.ActionResponse) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	id := queue.ActionSubmissionID{
		ActionID:      response.Result.ID,
		SubmissionTag: response.Result.SubmissionTag,
	}

	storingDur := rs.resultTTL
	if response.Result.SubmissionTag == types.Submit {
		storingDur = rs.submitResultTTL
	}

	// Status 0/1 is final (never overridden); >=2 is transient (overridden by a strictly higher transient or by any final).
	res, err := rs.s.Get(ctx, id.String())
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("reading existing response for %s: %w", id.String(), err)
	}
	if res != nil {
		if res.Result.Status < 2 {
			return fmt.Errorf("%w for %s", errOverrideFinal, id.String())
		}
		if response.Result.Status >= 2 && res.Result.Status >= response.Result.Status {
			return fmt.Errorf("%w for %s", errOverrideTransient, id.String())
		}
	}

	err = rs.s.SetWithTTL(ctx, id.String(), response, storingDur)
	if err != nil {
		return fmt.Errorf("storing response %s: %w", id.String(), err)
	}

	rs.n.Publish(ctx, id.String()) //nolint:errcheck // best-effort notification

	return nil
}

// FetchResponse returns action response for action id and submission tag.
func (rs *ResultStorage) FetchResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	return rs.fetchByID(ctx, queue.ActionSubmissionID{ActionID: actionID, SubmissionTag: submissionTag})
}

func (rs *ResultStorage) fetchByID(ctx context.Context, id queue.ActionSubmissionID) (*types.ActionResponse, error) {
	response, err := rs.s.Get(ctx, id.String())
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: response not in storage: %v", status.HTTP[404], err)
	}
	if err != nil {
		return nil, fmt.Errorf("reading response for %s: %w", id.String(), err)
	}

	return response, nil
}

// WaitOnResponse blocks until the response for actionID/submissionTag is stored, ctx is cancelled,
// or timeout elapses (whichever comes first). A non-positive timeout disables the timer; ctx is
// still honoured. Use only when the action is expected to be processed.
func (rs *ResultStorage) WaitOnResponse(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag, timeout time.Duration) (*types.ActionResponse, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	id := queue.ActionSubmissionID{ActionID: actionID, SubmissionTag: submissionTag}

	// Fast path: avoid the SUBSCRIBE round-trip when the result is already stored.
	if r, err := rs.fetchByID(ctx, id); err == nil {
		return r, nil
	}

	sub := rs.n.Subscribe(ctx, id.String())
	defer func() {
		if cerr := sub.Close(); cerr != nil {
			logger.Warnf("closing sub for %v: %v", id, cerr)
		}
	}()

	// Race-safe re-check: StoreResponse may have landed between the fast-path fetch and Subscribe.
	if r, err := rs.fetchByID(ctx, id); err == nil {
		return r, nil
	}

	_, err := sub.ReceiveMessage(ctx)
	if err != nil {
		// Hopeful attempt after error or context cancellation.
		if r, ferr := rs.fetchByID(ctx, id); ferr == nil {
			return r, nil
		}
		return nil, fmt.Errorf("waiting for the response for %v: %w", id, err)
	}

	return rs.fetchByID(ctx, id)
}
