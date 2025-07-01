package action

import (
	"context"

	"github.com/flare-foundation/tee-proxy/pkg/queue"

	"github.com/flare-foundation/tee-node/pkg/types"
)

type Service struct {
	*queue.ActionQueues
}

func (s *Service) DequeueAction(ctx context.Context, id queue.QueueID) (*types.Action, error) {
	action, err := s.Pop(ctx, id)
	if err != nil {
		return nil, err
	}

	return action, nil
}
