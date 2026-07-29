// Package machinepath provides the service that watches the C-chain indexer for
// governance-signed machine path lists and forwards them to the TEE node as
// SET_MACHINE_PATH_LIST direct actions.
package machinepath

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
	"gorm.io/gorm"
)

// responseWaitTimeout bounds how long a poll waits for the TEE node to confirm a
// submitted SET_MACHINE_PATH_LIST action before giving up and retrying on the
// next tick.
const responseWaitTimeout = 2 * time.Minute

// Service polls the indexer for newly signed machine path lists and enqueues a
// SET_MACHINE_PATH_LIST direct action for each list whose nonce advances past
// the last one submitted.
type Service struct {
	aq             *queue.ActionQueues
	responses      *result.ResultStorage
	managerAddress common.Address
	governance     types.Governance
	extensionID    common.Hash
	chainID        uint64

	metrics *metrics.Metrics

	lastNonce uint64
}

// NewService creates a machine path list service for the given extension. The
// manager address must be nonzero; the caller is responsible for skipping
// service creation when the feature is disabled. governance is the node's
// governance snapshot as reported in its TEE info; when it is Safe-backed
// (Safe nonzero), approveMachinePathList Safe transactions are collected and
// pre-verified as authorization evidence alongside direct governance
// signatures.
func NewService(aq *queue.ActionQueues, responses *result.ResultStorage, managerAddress common.Address, governance types.Governance, extensionID common.Hash, chainID uint64, initialNonce uint64, m *metrics.Metrics) *Service {
	return &Service{
		aq:             aq,
		responses:      responses,
		managerAddress: managerAddress,
		governance:     governance,
		extensionID:    extensionID,
		chainID:        chainID,
		lastNonce:      initialNonce,
		metrics:        m,
	}
}

// Run starts the polling loop. It returns immediately; the loop exits when ctx
// is cancelled.
func (s *Service) Run(ctx context.Context, db *gorm.DB, fetchInterval time.Duration) {
	go func() {
		ticker := time.NewTicker(fetchInterval)
		defer ticker.Stop()

		for {
			s.poll(ctx, db)

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// poll submits an action for the latest signed machine path list newer than the
// one last submitted, if any.
func (s *Service) poll(ctx context.Context, db *gorm.DB) {
	nonce, toBlock, found, err := machinepath.LatestSignedList(ctx, db, s.managerAddress, s.extensionID, s.lastNonce)
	if err != nil {
		logger.Warnf("fetching latest signed machine path list for extension %s: %v", s.extensionID, err)
		s.metrics.MachinepathPollObserved("fetch_error")
		return
	}
	if !found {
		logger.Debugf("no machine path list newer than nonce %d for extension %s", s.lastNonce, s.extensionID)
		s.metrics.MachinepathPollObserved("no_change")
		return
	}

	action, err := machinepath.SetMachinePathListAction(ctx, db, s.managerAddress, s.governance, s.extensionID, s.chainID, nonce, toBlock)
	if err != nil {
		logger.Warnf("creating SET_MACHINE_PATH_LIST action for nonce %d: %v", nonce, err)
		// missing authorization evidence is a governance-config condition, not an infra fault
		outcome := "build_error"
		if errors.Is(err, machinepath.ErrNoAuthorization) {
			outcome = "no_authorization"
		}
		s.metrics.MachinepathPollObserved(outcome)
		return
	}

	if err := s.aq.Enqueue(ctx, action, processorutils.Direct); err != nil {
		logger.Warnf("enqueueing SET_MACHINE_PATH_LIST action for nonce %d: %v", nonce, err)
		s.metrics.MachinepathPollObserved("enqueue_error")
		return
	}

	// Advance lastNonce only once the TEE node confirms the action succeeded;
	// otherwise the next poll retries the same list.
	start := time.Now()
	response, err := s.responses.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, responseWaitTimeout)
	s.metrics.ObserveNodeWait("machinepath", time.Since(start), err)
	if err != nil {
		logger.Warnf("waiting for SET_MACHINE_PATH_LIST response for nonce %d: %v", nonce, err)
		// a shutdown cancellation is not a poll failure
		if !errors.Is(err, context.Canceled) {
			s.metrics.MachinepathPollObserved("wait_error")
		}
		return
	}
	if response.Result.Status != 1 {
		logger.Warnf("SET_MACHINE_PATH_LIST action for nonce %d failed: %s", nonce, response.Result.Log)
		s.metrics.MachinepathPollObserved("rejected")
		return
	}

	s.lastNonce = nonce
	s.metrics.MachinepathPollObserved("confirmed")
	logger.Infof("confirmed machine path list nonce %d for extension %s", nonce, s.extensionID)
}
