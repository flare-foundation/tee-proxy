package info

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/flare-foundation/tee-proxy/pkg/attestation"
	"github.com/flare-foundation/tee-proxy/pkg/config"

	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"

	"gorm.io/gorm"
)

// Service holds the latest TEE info and manages updating it.
//
// When you are accessing Latest or LastUpdate mutex should be used.
type Service struct {
	Latest      *types.TeeInfoResponse
	LastUpdated time.Time

	// lastAttestationErr is sticky: once set, never cleared. A failed attestation
	// indicates possible compromise; the orchestrator should restart the pod.
	lastAttestationErr error

	db *gorm.DB

	actionQueues    *queue.ActionQueues
	responseStorage *result.ResultStorage

	timingConfig   *config.InfoTiming
	attestationCfg *attestation.Config

	metrics *metrics.Metrics

	sync.RWMutex
}

// NewService creates an info Service that periodically refreshes TEE info from the tee-node
// and, when ac.Enabled, verifies the response's attestation. m may be nil or disabled.
// LastUpdated starts at construction time so info_service_delay_seconds reports real elapsed
// time from boot instead of a multi-decade spike before the first refresh.
func NewService(db *gorm.DB, aq *queue.ActionQueues, rs *result.ResultStorage, tc *config.InfoTiming, ac *attestation.Config, m *metrics.Metrics) *Service {
	return &Service{
		Latest:      new(types.TeeInfoResponse),
		LastUpdated: time.Now(),

		db:              db,
		actionQueues:    aq,
		responseStorage: rs,
		timingConfig:    tc,
		attestationCfg:  ac,
		metrics:         m,
	}
}

// LastAttestationErr returns the sticky attestation verification error, or nil if no failure has occurred.
func (s *Service) LastAttestationErr() error {
	s.RLock()
	defer s.RUnlock()
	return s.lastAttestationErr
}

// LastAppliedPolicyID returns the reward epoch ID of the signing policy the tee-node
// most recently reported as active.
func (s *Service) LastAppliedPolicyID() uint32 {
	s.RLock()
	defer s.RUnlock()
	if s.Latest == nil {
		return 0
	}
	return s.Latest.TeeInfo.LastSigningPolicyID
}

// LastGovernanceHash returns the governance hash the tee-node most recently
// reported in its machine data.
func (s *Service) LastGovernanceHash() common.Hash {
	s.RLock()
	defer s.RUnlock()
	if s.Latest == nil {
		return common.Hash{}
	}
	return s.Latest.MachineData.GovernanceHash
}

// Run starts the periodic update of TEE info.
func (s *Service) Run(ctx context.Context) error {
	errCount := 0
	ticker := time.NewTicker(s.timingConfig.CycleInternal)
	defer ticker.Stop()

	// config validation guarantees ≥ 1; the clamp keeps a hand-built InfoTiming
	// from turning a cycle into an unbounded retry loop
	attempts := max(s.timingConfig.MaxAttempts, 1)

	for {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			logger.Info("tee info storage exiting")
			return ctx.Err()
		}

		_, err := s.retryUpdate(ctx, s.timingConfig.CycleQueueResponseWait, attempts)
		switch {
		case err == nil:
			errCount = 0
		case ctx.Err() != nil: // shutting down, not a refresh failure
		default:
			errCount++
			// a failed cycle has already spent every attempt, so the first one is warned about
			if errCount == 1 || errCount%30 == 0 {
				logger.Warnf("tee info update unsuccessful in %d consecutive cycles: latest error: %v", errCount, err)
			} else {
				logger.Debugf("tee info update failed (cycle %d): %v", errCount, err)
			}
		}
	}
}

// FetchInfo updates info and returns the update along with the challenge that was sent.
// It retries until it succeeds or budget is spent; budget 0 means retry indefinitely.
// Callers verify TeeInfo.Challenge against the returned challenge to confirm the response answers their request.
func (s *Service) FetchInfo(ctx context.Context, budget time.Duration) (*types.TeeInfoResponse, common.Hash, error) {
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	// no attempt limit: budget is the only bound, so a node that is slow to attest
	// (cold launcher at boot) does not take the proxy down with it
	challenge, err := s.retryUpdate(ctx, s.timingConfig.CycleQueueResponseWait, 0)
	if err != nil {
		return nil, common.Hash{}, err
	}

	return s.Latest, challenge, nil
}

// retryUpdate refreshes the info, retrying an attempt that failed on an operational stage.
// attempts bounds the tries; attempts < 1 retries until ctx ends. wait bounds each attempt's
// wait for the node's response.
func (s *Service) retryUpdate(ctx context.Context, wait time.Duration, attempts int) (common.Hash, error) {
	for attempt := 1; ; attempt++ {
		challenge, err := s.updateInfo(ctx, wait)
		if err == nil {
			return challenge, nil
		}

		spent := attempts >= 1 && attempt >= attempts
		if spent || !retryableStage(err) || ctx.Err() != nil {
			if ctx.Err() == nil {
				s.metrics.InfoRefreshExhausted(refreshStage(err))
			}
			if attempt > 1 {
				return common.Hash{}, fmt.Errorf("after %d attempts: %w", attempt, err)
			}
			return common.Hash{}, err
		}

		// an unbounded retry is the bootstrap fetch: no per-cycle warning above it, and a
		// boot that keeps retrying must not be silent
		if attempts < 1 {
			logger.Warnf("tee info attempt %d failed, retrying in %v: %v", attempt, s.timingConfig.RetryDelay, err)
		} else {
			logger.Debugf("tee info attempt %d failed, retrying in %v: %v", attempt, s.timingConfig.RetryDelay, err)
		}

		select {
		case <-time.After(s.timingConfig.RetryDelay):
		case <-ctx.Done():
			// keep the last failure: on a spent bootstrap budget it is the reason the proxy dies
			return common.Hash{}, fmt.Errorf("after %d attempts, %w: last error: %v", attempt, ctx.Err(), err)
		}
	}
}

// stageError tags a refresh failure with the pipeline stage that produced it.
type stageError struct {
	stage string
	err   error
}

func (e *stageError) Error() string { return e.err.Error() }

func (e *stageError) Unwrap() error { return e.err }

// stages whose failure is a security signal rather than a transient one: a retry cannot fix it,
// and re-verifying multiplies the page the first occurrence already raised.
var nonRetryableStages = map[string]bool{"verify_signature": true, "verify_attestation": true}

// fail counts a refresh failure at stage and tags err with it.
func (s *Service) fail(stage string, err error) error {
	s.metrics.InfoRefreshFailed(stage)

	return &stageError{stage: stage, err: err}
}

// refreshStage returns the pipeline stage err comes from; "unknown" for anything not raised by an attempt.
func refreshStage(err error) string {
	var se *stageError
	if errors.As(err, &se) {
		return se.stage
	}

	return "unknown"
}

func retryableStage(err error) bool {
	return !nonRetryableStages[refreshStage(err)]
}

// newInfoAction returns an action with opType GET, opCommand TEE_INFO,
// and challenge in tee info request.
func newInfoAction(challenge common.Hash) (*types.Action, error) {
	m := types.TeeInfoRequest{
		Challenge: challenge,
	}

	msg, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	return queue.PrepareDirectAction(op.Get, op.TEEInfo, msg)
}

// updateInfo updates the latest info by sending a TEE_INFO action to the TEE and waiting for the response.
// Returns the challenge that was sent so callers can verify the response binds to it.
// Every failure carries its pipeline stage, which decides retryability; see retryUpdate.
func (s *Service) updateInfo(ctx context.Context, timeout time.Duration) (_ common.Hash, err error) {
	refreshStart := time.Now()
	defer func() { s.metrics.InfoRefreshObserved(time.Since(refreshStart), err) }()

	block, err := database.FetchLatestBlock(ctx, s.db, nil)
	if err != nil {
		return common.Hash{}, s.fail("fetch_block", fmt.Errorf("fetching latest block: %w", err))
	}

	challenge := common.HexToHash(block.Hash)

	action, err := newInfoAction(challenge)
	if err != nil {
		return common.Hash{}, s.fail("create_action", fmt.Errorf("creating info action: %w", err))
	}

	err = s.actionQueues.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return common.Hash{}, s.fail("enqueue", fmt.Errorf("enqueueing info action: %w", err))
	}

	start := time.Now()
	response, err := s.responseStorage.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, timeout)
	s.metrics.ObserveNodeWait("info", time.Since(start), err)
	if err != nil {
		return common.Hash{}, s.fail("wait_response", fmt.Errorf("waiting for info response: %w", err))
	}
	if response.Result.Status != 1 {
		// the node reports a failed attestation this way, e.g. when its token endpoint is slow
		return common.Hash{}, s.fail("action_status", fmt.Errorf("TEE_INFO action failed: %s", response.Result.Log))
	}

	var result types.TeeInfoResponse

	err = json.Unmarshal(response.Result.Data, &result)
	if err != nil {
		return common.Hash{}, s.fail("unmarshal", fmt.Errorf("unmarshaling info response: %w", err))
	}

	if s.attestationCfg != nil {
		teeID, err := ParseTeeID(&result)
		if err != nil {
			return common.Hash{}, s.fail("parse_tee_id", fmt.Errorf("parsing tee ID: %w", err))
		}

		signingHash, err := signing.NewPayload(signing.TEEActionResult, result.TeeInfo.ChainID, [32]byte(response.Result.Hash())).Hash()
		if err != nil {
			return common.Hash{}, s.fail("signing_hash", fmt.Errorf("computing signing hash: %w", err))
		}

		err = utils.VerifySignature(signingHash[:], response.Signature, teeID)
		if err != nil {
			// set-site Warn: the alert pages on one occurrence, and this failure is never retried
			logger.Warnf("TEE info response signature verification failed: %v", err)
			return common.Hash{}, s.fail("verify_signature", fmt.Errorf("verifying response signature: %w", err))
		}

		vErr := attestation.Verify(&result, challenge, s.attestationCfg)
		if s.attestationCfg.Enabled {
			res, reason := attestationOutcome(&result, vErr)
			s.metrics.AttestationVerified(res, reason)
		}
		if vErr != nil {
			s.Lock()
			changed := s.lastAttestationErr == nil || s.lastAttestationErr.Error() != vErr.Error()
			s.lastAttestationErr = vErr
			s.Unlock()
			if changed {
				logger.Warnf("attestation verification failed (sticky, readiness fails until restart): %v", vErr)
			}
			return common.Hash{}, s.fail("verify_attestation", fmt.Errorf("verifying attestation: %w", vErr))
		}
	}

	s.Lock()
	defer s.Unlock()

	if s.Latest == nil {
		s.Latest = new(types.TeeInfoResponse)
	}
	*s.Latest = result
	s.LastUpdated = time.Now()

	return challenge, nil
}

// attestationOutcome maps a completed attestation verification to its bounded metric labels.
// An accepted magic_pass sentinel (nil error under AllowMagicPass) yields result "ok" with
// reason ReasonMagicPass, keeping it distinguishable from a genuine JWT pass; any error yields
// result "error" with the classified Reason. The vErr==nil gate guarantees the sentinel was
// actually accepted — a magic_pass under AllowMagicPass=false returns ErrMagicPassDisabled and
// takes the error branch, so IsMagicPass is only consulted after acceptance.
func attestationOutcome(tir *types.TeeInfoResponse, vErr error) (result, reason string) {
	if vErr != nil {
		return "error", attestation.Reason(vErr)
	}
	if attestation.IsMagicPass(tir) {
		return "ok", attestation.ReasonMagicPass
	}
	return "ok", attestation.Reason(nil)
}

// ParseTeeID returns the TEE identity: the address derived from the TEE public key in the response.
func ParseTeeID(tir *types.TeeInfoResponse) (common.Address, error) {
	teePub, err := types.ParsePubKey(tir.TeeInfo.PublicKey)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*teePub), nil
}
