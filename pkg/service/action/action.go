package action

import (
	"context"

	"github.com/flare-foundation/tee-proxy/pkg/redis"

	"github.com/flare-foundation/tee-node/pkg/types"
)

type Service struct {
	*redis.ActionStorage
}

func (s *Service) DequeueAction(ctx context.Context, id redis.QueueID) (*types.Action, error) {
	action, err := s.Pop(ctx, id)
	if err != nil {
		return nil, err
	}

	return action, nil
}
