package policy

import (
	"context"
	"errors"

	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"gorm.io/gorm"
)

type Service struct {
	aq        *queue.ActionQueues
	addresses config.Addresses

	activePolicy *cpolicy.SigningPolicy
}

func NewService(aq *queue.ActionQueues, addresses config.Addresses) *Service {
	return &Service{
		aq:        aq,
		addresses: addresses,
	}
}

func (s *Service) Initialize(ctx context.Context, db *gorm.DB, offset int) error {
	action, p, actualOffset, err := policy.InitializePolicyAction(ctx, db, s.addresses, offset)
	if err != nil {
		return err
	}

	if actualOffset != offset {
		logger.Warnf("policy initialization set for offset: %d, actual offset: %d", offset, actualOffset)
	}

	s.activePolicy = p

	logger.Infof("initialized for policy %d", p.RewardEpochID)

	return s.aq.Enqueue(ctx, action, processorutils.Direct)
}

func (s *Service) Run(ctx context.Context, db *gorm.DB) (<-chan cpolicy.SigningPolicy, error) {
	if s.activePolicy == nil {
		return nil, errors.New("not initialized yet")
	}

	startID := s.activePolicy.RewardEpochID + 1

	logChan, err := policy.SigningPolicyInitializedEventsListener(ctx, db, s.addresses.Relay, startID)
	if err != nil {
		return nil, err
	}

	pChan := s.update(ctx, db, logChan)

	return pChan, nil
}

func (s *Service) SetInitialPolicy(ctx context.Context, db *gorm.DB, signingPolicyID uint32) error {
	if s.activePolicy != nil {
		return errors.New("initial policy already set")
	}

	p, err := policy.FetchSigningPolicy(ctx, db, s.addresses.Relay, signingPolicyID)
	if err != nil {
		return err
	}

	s.activePolicy = p
	return nil
}

func (s *Service) update(ctx context.Context, db *gorm.DB, logsC <-chan []database.Log) <-chan cpolicy.SigningPolicy {
	out := make(chan cpolicy.SigningPolicy, 1)

	if s.activePolicy != nil {
		out <- *s.activePolicy
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case logs := <-logsC:
				logger.Debug("updating signing policy from")
				action, p, err := policy.UpdatePolicyAction(ctx, db, s.addresses, logs[0], s.activePolicy)
				if err != nil {
					logger.Errorf("creating UPDATE_POLICY action: %s", err)
					continue
				}

				s.activePolicy = p

				out <- *p

				err = s.aq.Enqueue(ctx, action, processorutils.Direct)
				if err != nil {
					logger.Errorf("enqueueing UPDATE_POLICY action: %s", err)
					continue
				}
			}
		}
	}()

	return out
}
