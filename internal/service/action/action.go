package action

import (
	"context"

	"github.com/flare-foundation/tee-proxy/pkg/queue"

	"github.com/flare-foundation/tee-node/pkg/types"
)

type Service struct {
	aq *queue.ActionQueues
}

func NewService(aq *queue.ActionQueues) Service {
	return Service{aq}
}

func (s *Service) DequeueAction(ctx context.Context, id queue.QueueID) (*types.Action, error) {
	action, err := s.aq.Pop(ctx, id)
	if err != nil {
		return nil, err
	}

	return action, nil
}
