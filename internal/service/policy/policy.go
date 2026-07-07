package policy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/retry"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
	"gorm.io/gorm"
)

// nodePolicyState reports the reward epoch of the signing policy the tee-node has most
// recently applied. Implemented by the info service; used to reconcile the proxy's
// active policy with the node after a missed update confirmation.
type nodePolicyState interface {
	LastAppliedPolicyID() uint32
}

// Service fetches signing policies from the blockchain and distributes them via action queues.
type Service struct {
	aq          *queue.ActionQueues
	responses   *result.ResultStorage
	scAddresses config.Addresses
	chainID     *big.Int
	nodeState   nodePolicyState
	metrics     *metrics.Metrics

	activePolicy *cpolicy.SigningPolicy

	// restartPolicies holds policies loaded on restart that must be
	// emitted on pChan (for the instruction service's cyclic buffer)
	// but NOT sent to the node.  Nil on first init.
	restartPolicies []*cpolicy.SigningPolicy
}

// NewService creates a new policy Service with the given action queues, result storage,
// contract addresses, chain ID, a view of the node's applied policy state, and metrics.
// m may be nil or disabled.
func NewService(aq *queue.ActionQueues, responses *result.ResultStorage, addresses config.Addresses, chainID uint64, nodeState nodePolicyState, m *metrics.Metrics) *Service {
	return &Service{
		aq:          aq,
		responses:   responses,
		scAddresses: addresses,
		chainID:     new(big.Int).SetUint64(chainID),
		nodeState:   nodeState,
		metrics:     m,
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
		var startID uint32
		if lastID > 0 {
			startID = lastID - 1
		}

		lastPolicy, err := policy.FetchSigningPolicy(ctx, db, s.scAddresses.Relay, lastID)
		if err != nil {
			return fmt.Errorf("loading last policy %d: %w", lastID, err)
		}

		prevPolicy := lastPolicy
		if startID != lastID {
			prevPolicy, err = policy.FetchSigningPolicy(ctx, db, s.scAddresses.Relay, startID)
			if err != nil {
				return fmt.Errorf("loading previous policy %d: %w", startID, err)
			}
		}

		s.activePolicy = lastPolicy
		s.metrics.SetActiveRewardEpoch(lastPolicy.RewardEpochID)
		s.restartPolicies = []*cpolicy.SigningPolicy{prevPolicy, lastPolicy}

		logger.Infof("restart: loaded policies %d and %d (updates start from %d)", startID, lastID, lastID+1)

		return nil
	}

	logger.Info("initializing signing policy")

	action, p, actualOffset, err := policy.InitializePolicyAction(ctx, db, s.scAddresses, offset, s.chainID)
	if err != nil {
		return fmt.Errorf("creating initialize policy action for chainID %v: %w", s.chainID, err)
	}

	if actualOffset != offset {
		logger.Warnf("policy initialization set for offset: %d, actual offset: %d", offset, actualOffset)
	}

	s.activePolicy = p
	s.metrics.SetActiveRewardEpoch(p.RewardEpochID)

	logger.Infof("initialized for policy %d", p.RewardEpochID)

	return s.aq.Enqueue(ctx, action, processorutils.Direct)
}

// Run starts the signing policy update loop and returns a channel that emits new signing policies.
func (s *Service) Run(ctx context.Context, db *gorm.DB, policyFetchInterval time.Duration) (<-chan cpolicy.SigningPolicy, error) {
	if s.activePolicy == nil {
		return nil, errors.New("not initialized yet")
	}

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
	go s.update(ctx, pChan, db, policyFetchInterval)

	return pChan, nil
}

const (
	// confirmationWaitTimeout bounds how long the update loop waits for the tee-node to
	// confirm an UPDATE_POLICY before retrying. It must stay well under the result
	// storage TTL so the confirmation is still available when awaited.
	confirmationWaitTimeout = 2 * time.Minute

	// warnAfterAttempts is the number of consecutive failed attempts on a single epoch
	// after which failure logs escalate from Info to Warn.
	warnAfterAttempts = 3
)

var (
	// updateRetryDelay is the pause before re-attempting the same epoch after a failed
	// attempt (signatures not yet collectable, node rejection, or transient error).
	updateRetryDelay = 1 * time.Minute

	// enqueueRetry bounds retries of the transient action enqueue; the outer update loop
	// provides durable, unbounded retry of the epoch itself.
	enqueueRetry = retry.Params{MaxAttempts: 5, Delay: time.Second}
)

// update drives signing-policy rollovers off activePolicy. Each iteration targets
// activePolicy.RewardEpochID+1 and does not advance until that exact epoch has been
// built, enqueued, and confirmed applied by the node. A failed epoch is retried
// indefinitely (never skipped): both the proxy's signature collection and the node
// require strictly consecutive policies, so skipping any epoch would wedge the pipeline.
func (s *Service) update(ctx context.Context, out chan cpolicy.SigningPolicy, db *gorm.DB, fetchInterval time.Duration) {
	attempts := 0
	for {
		if ctx.Err() != nil {
			return
		}

		// Reconcile with the node: if it has already applied a later policy than we
		// consider active (e.g. we enqueued an UPDATE_POLICY the node applied but whose
		// confirmation we missed), adopt the node's state rather than re-submitting an
		// epoch it already has.
		if nodeID := s.nodeState.LastAppliedPolicyID(); nodeID > s.activePolicy.RewardEpochID {
			p, err := policy.FetchSigningPolicy(ctx, db, s.scAddresses.Relay, nodeID)
			if err != nil {
				logger.Errorf("reconciling active signing policy to node's %d: %v", nodeID, err)
				if !wait(ctx, fetchInterval) {
					return
				}
				continue
			}
			logger.Infof("reconciled active signing policy to node's %d (was %d)", nodeID, s.activePolicy.RewardEpochID)
			s.activePolicy = p
			s.metrics.SetActiveRewardEpoch(p.RewardEpochID)
			attempts = 0
			if !emit(ctx, out, p) {
				return
			}
			continue
		}

		target := s.activePolicy.RewardEpochID + 1

		log, found, err := policy.FetchSigningPolicyLog(ctx, db, s.scAddresses.Relay, target)
		if err != nil {
			logger.Errorf("fetching signing policy %d log: %v", target, err)
			if !wait(ctx, fetchInterval) {
				return
			}
			continue
		}
		if !found {
			// The next epoch is not yet initialized on chain — the steady-state path.
			logger.Debugf("signing policy %d not yet on chain; waiting", target)
			if !wait(ctx, fetchInterval) {
				return
			}
			continue
		}

		attempts++

		action, newPolicy, err := policy.UpdatePolicyAction(ctx, db, s.scAddresses, log, s.activePolicy, s.chainID)
		if err != nil {
			logUpdateFailure(target, attempts, "building UPDATE_POLICY action", err)
			if !wait(ctx, updateRetryDelay) {
				return
			}
			continue
		}

		es := retry.Execute(ctx, func() (struct{}, error) {
			return struct{}{}, s.aq.Enqueue(ctx, action, processorutils.Direct)
		}, enqueueRetry)
		if !es.Success {
			logUpdateFailure(target, attempts, "enqueueing UPDATE_POLICY action", es.Err)
			if !wait(ctx, updateRetryDelay) {
				return
			}
			continue
		}

		// Advance only after the node confirms it applied the policy, so proxy and node
		// stay in lockstep (both enforce strictly consecutive epochs).
		resp, err := s.responses.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, confirmationWaitTimeout)
		if err != nil {
			logUpdateFailure(target, attempts, "awaiting UPDATE_POLICY confirmation", err)
			if !wait(ctx, updateRetryDelay) {
				return
			}
			continue
		}
		if resp.Result.Status != 1 {
			logUpdateFailure(target, attempts, fmt.Sprintf("node rejected UPDATE_POLICY (status %d, log %q)", resp.Result.Status, resp.Result.Log), nil)
			if !wait(ctx, updateRetryDelay) {
				return
			}
			continue
		}

		s.activePolicy = newPolicy
		s.metrics.SetActiveRewardEpoch(newPolicy.RewardEpochID)
		attempts = 0
		logger.Infof("updated signing policy to %d", newPolicy.RewardEpochID)
		if !emit(ctx, out, newPolicy) {
			return
		}
	}
}

// emit sends p on out, or returns false if ctx is cancelled first.
func emit(ctx context.Context, out chan cpolicy.SigningPolicy, p *cpolicy.SigningPolicy) bool {
	select {
	case out <- *p:
		return true
	case <-ctx.Done():
		return false
	}
}

// logUpdateFailure logs a failed attempt to apply target. Early attempts (typically
// waiting for signatures to appear on chain during a rollover) are expected and log at
// Info; sustained failures escalate to Warn so a genuinely stuck rollover is visible.
func logUpdateFailure(target uint32, attempts int, stage string, err error) {
	msg := fmt.Sprintf("applying signing policy %d (attempt %d): %s", target, attempts, stage)
	if err != nil {
		msg = fmt.Sprintf("%s: %v", msg, err)
	}
	if attempts >= warnAfterAttempts {
		logger.Warn(msg)
	} else {
		logger.Info(msg)
	}
}

// wait sleeps for d, returning false if ctx is cancelled first.
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
