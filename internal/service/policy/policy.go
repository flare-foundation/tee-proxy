package policy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
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

// Service fetches signing policies from the blockchain and distributes them via action queues.
type Service struct {
	aq          *queue.ActionQueues
	scAddresses config.Addresses
	chainID     *big.Int

	activePolicy *cpolicy.SigningPolicy

	// restartPolicies holds policies loaded on restart that must be
	// emitted on pChan (for the instruction service's cyclic buffer)
	// but NOT sent to the node.  Nil on first init.
	restartPolicies []*cpolicy.SigningPolicy
}

// NewService creates a new policy Service with the given action queues, contract addresses, and chain ID.
func NewService(aq *queue.ActionQueues, addresses config.Addresses, chainID *big.Int) *Service {
	return &Service{
		aq:          aq,
		scAddresses: addresses,
		chainID:     chainID,
	}
}

// Initialize prepares the service for operation by loading the active signing policy from the database.
// On a fresh start it submits an INITIALIZE_POLICY action; on restart it loads existing policies from the node.
func (s *Service) Initialize(ctx context.Context, db *gorm.DB, offset int, teeInitialInfo *types.TeeInfoResponse) error {
	if teeInitialInfo.TeeInfo.InitialSigningPolicyHash.Cmp(common.Hash{}) != 0 {
		lastID := teeInitialInfo.TeeInfo.LastSigningPolicyID

		// On restart the node already has policies up to lastID.
		// Load lastID-1 and lastID so the instruction service's cyclic
		// buffer has rounds for both the current and previous epoch
		// (needed during the ~2h window when a new policy is initialized
		// but the old epoch is still active).
		//
		// Neither policy is sent to the node — it already has them.
		// activePolicy is set to lastID so that UpdatePolicyAction
		// collects signatures from the correct voters for lastID+1.
		startID := lastID - 1
		if lastID == 0 {
			startID = 0
		}

		prevPolicy, err := policy.FetchSigningPolicy(ctx, db, s.scAddresses.Relay, startID)
		if err != nil {
			return fmt.Errorf("setting initial policy: %w", err)
		}
		lastPolicy, err := policy.FetchSigningPolicy(ctx, db, s.scAddresses.Relay, lastID)
		if err != nil {
			return err
		}

		s.activePolicy = lastPolicy
		s.restartPolicies = []*cpolicy.SigningPolicy{prevPolicy, lastPolicy}

		logger.Infof("restart: loaded policies %d and %d (listener starts from %d)", startID, lastID, lastID+1)

		return nil
	}

	logger.Info("initializing signing policy")

	action, p, actualOffset, err := policy.InitializePolicyAction(ctx, db, s.scAddresses, offset, s.chainID)
	if err != nil {
		return fmt.Errorf("creating initialize policy action: %w", err)
	}

	if actualOffset != offset {
		logger.Warnf("policy initialization set for offset: %d, actual offset: %d", offset, actualOffset)
	}

	s.activePolicy = p

	logger.Infof("initialized for policy %d", p.RewardEpochID)

	return s.aq.Enqueue(ctx, action, processorutils.Direct)
}

// Run starts the signing policy update loop and returns a channel that emits new signing policies.
func (s *Service) Run(ctx context.Context, db *gorm.DB, policyFetchInterval time.Duration) (<-chan cpolicy.SigningPolicy, error) {
	if s.activePolicy == nil {
		return nil, errors.New("not initialized yet")
	}

	startID := s.activePolicy.RewardEpochID + 1

	logChan := signingPolicyInitializedEventsListener(ctx, db, s.scAddresses.Relay, startID, policyFetchInterval)

	pChan := make(chan cpolicy.SigningPolicy, 3)
	if s.restartPolicies != nil {
		// On restart: emit previously loaded policies so the instruction
		// service creates cyclic buffer rounds for them.  These are NOT
		// sent to the node (which already has them).
		for _, p := range s.restartPolicies {
			pChan <- *p
		}
		s.restartPolicies = nil
	} else {
		pChan <- *s.activePolicy
	}
	go s.update(ctx, pChan, db, logChan)

	return pChan, nil
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

			logger.Debugf("updating signing policy from %d", s.activePolicy.RewardEpochID)
			action, p, err := policy.UpdatePolicyAction(ctx, db, s.scAddresses, log, s.activePolicy, s.chainID)
			if err != nil {
				logger.Errorf("creating UPDATE_POLICY action: %v", err)
				continue
			}

			s.activePolicy = p
			logger.Debugf("updated signing policy to %d", s.activePolicy.RewardEpochID)

			out <- *p

			err = s.aq.Enqueue(ctx, action, processorutils.Direct)
			if err != nil {
				logger.Errorf("enqueueing UPDATE_POLICY action: %v", err)
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
) <-chan database.Log {
	out := make(chan database.Log, 1)

	policyID := startPolicyID

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			topics := [4]common.Hash{}
			topics[0] = policy.SigningPolicyInitializedEventSel
			topics[1] = convert.Uint32ToHash(policyID)

			params := database.LogsFullParams{
				Address: relayAddress,
				Topics:  topics,
				Number:  1,
			}

			logs, err := database.FetchLogsFull(ctx, db, params)
			if err != nil {
				logger.Errorf("fetching logs: %v", err)
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

	return out
}
