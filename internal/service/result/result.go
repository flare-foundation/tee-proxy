package result

import (
	"context"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
)

type Service struct {
	*queue.ResponseStorage
}

// Serve returns response for actionID with tag "threshold" if present.
func (s *Service) Serve(ctx context.Context, actionID common.Hash) (*types.ActionResponse, error) {
	return s.GetResponse(ctx, actionID, types.Threshold)
}

// Serve returns response for actionID with tag "end" if present.
func (s *Service) ServeRewards(ctx context.Context, actionID common.Hash) (*types.ActionResponse, error) {
	return s.GetResponse(ctx, actionID, types.End)
}
