package policy

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
	"gorm.io/gorm"
)

type Service struct {
	aq          *queue.ActionQueues
	scAddresses config.Addresses

	activePolicy *cpolicy.SigningPolicy
}

func NewService(aq *queue.ActionQueues, addresses config.Addresses) *Service {
	return &Service{
		aq:          aq,
		scAddresses: addresses,
	}
}

func (s *Service) Initialize(ctx context.Context, db *gorm.DB, offset int, teeInitialInfo *types.TeeInfoResponse) error {
	if teeInitialInfo.TeeInfo.InitialSigningPolicyHash.Cmp(common.Hash{}) != 0 {
		logger.Infof("starting signing policy updates from epoch %d", teeInitialInfo.TeeInfo.LastSigningPolicyID)
		err := s.SetInitialPolicy(ctx, db, teeInitialInfo.TeeInfo.LastSigningPolicyID)
		if err != nil {
			return err
		}

		return nil
	}

	logger.Info("initializing signing policy")

	action, p, actualOffset, err := policy.InitializePolicyAction(ctx, db, s.scAddresses, offset)
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

func (s *Service) Run(ctx context.Context, db *gorm.DB, policyFetchInterval time.Duration) (<-chan cpolicy.SigningPolicy, error) {
	if s.activePolicy == nil {
		return nil, errors.New("not initialized yet")
	}

	startID := s.activePolicy.RewardEpochID + 1

	logChan, err := signingPolicyInitializedEventsListener(ctx, db, s.scAddresses.Relay, startID, policyFetchInterval)
	if err != nil {
		return nil, err
	}

	pChan := make(chan cpolicy.SigningPolicy, 1)
	if s.activePolicy != nil {
		pChan <- *s.activePolicy
	}
	go s.update(ctx, pChan, db, logChan)

	return pChan, nil
}

func (s *Service) SetInitialPolicy(ctx context.Context, db *gorm.DB, signingPolicyID uint32) error {
	if s.activePolicy != nil {
		return errors.New("initial policy already set")
	}

	p, err := policy.FetchSigningPolicy(ctx, db, s.scAddresses.Relay, signingPolicyID)
	if err != nil {
		return err
	}

	s.activePolicy = p
	return nil
}

// update receives SigningPolicyInitialized events from logsC and:
//   - processes them into SigningPolicy structs
//   - adds signing policy to out channel
//   - fetches public keys of signers in the signing active
//   - creates and enqueues "UPDATE_POLICY" actions.
func (s *Service) update(ctx context.Context, out chan cpolicy.SigningPolicy, db *gorm.DB, logsC <-chan database.Log) {
	for {
		select {
		case <-ctx.Done():
			return
		case log := <-logsC:

			logger.Debug("updating signing policy from")
			action, p, err := policy.UpdatePolicyAction(ctx, db, s.scAddresses, log, s.activePolicy)
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
}

func signingPolicyInitializedEventsListener(
	ctx context.Context,
	db *gorm.DB,
	relayAddress common.Address,
	startPolicyID uint32,
	fetchInterval time.Duration,
) (<-chan database.Log, error) {
	out := make(chan database.Log, 1)

	policyID := startPolicyID

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			topics := [4]common.Hash{}
			topics[0] = policy.SigningPolicyInitializedEventSel
			topics[1] = policy.Uint32ToHash(policyID)

			params := database.LogsFullParams{
				Address: relayAddress,
				Topics:  topics,
				Number:  1,
			}

			logs, err := database.FetchLogsFull(ctx, db, params)
			if err != nil {
				logger.Error("fetch logs error:", err)
				continue
			}

			if len(logs) > 0 {
				if len(logs) > 1 {
					// this should never happen
					logger.Warnf("received more than one log for signing policy initialized ID %d", policyID)
				}

				out <- logs[0]
				policyID++
				continue
			}

			time.Sleep(fetchInterval)
		}
	}()

	return out, nil
}
