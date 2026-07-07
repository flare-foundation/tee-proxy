package info

import (
	"context"
	"encoding/json"
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
func NewService(db *gorm.DB, aq *queue.ActionQueues, rs *result.ResultStorage, tc *config.InfoTiming, ac *attestation.Config, m *metrics.Metrics) *Service {
	return &Service{
		Latest:      new(types.TeeInfoResponse),
		LastUpdated: time.Unix(0, 0),

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

// Run starts the periodic update of TEE info.
func (s *Service) Run(ctx context.Context) error {
	errCount := 0
	ticker := time.NewTicker(s.timingConfig.CycleInternal)

	for {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			logger.Info("tee info storage exiting")
			return ctx.Err()
		}
		_, err := s.updateInfo(ctx, s.timingConfig.CycleQueueResponseWait)
		if err != nil {
			errCount++
		} else {
			errCount = 0
		}

		if errCount > 5 {
			logger.Errorf("tee info update unsuccessful in %d attempts: latest error: %v", errCount, err)
		}
	}
}

// FetchInfo updates info and returns the update along with the challenge that was sent.
// Callers verify TeeInfo.Challenge against the returned challenge to confirm the response answers their request.
func (s *Service) FetchInfo(ctx context.Context, timeout time.Duration) (*types.TeeInfoResponse, common.Hash, error) {
	challenge, err := s.updateInfo(ctx, timeout)
	if err != nil {
		return nil, common.Hash{}, err
	}

	return s.Latest, challenge, nil
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
func (s *Service) updateInfo(ctx context.Context, timeout time.Duration) (_ common.Hash, err error) {
	refreshStart := time.Now()
	defer func() { s.metrics.InfoRefreshObserved(time.Since(refreshStart), err) }()

	block, err := database.FetchLatestBlock(ctx, s.db, nil)
	if err != nil {
		s.metrics.InfoRefreshFailed("fetch_block")
		return common.Hash{}, fmt.Errorf("fetching latest block: %w", err)
	}

	challenge := common.HexToHash(block.Hash)

	action, err := newInfoAction(challenge)
	if err != nil {
		s.metrics.InfoRefreshFailed("create_action")
		return common.Hash{}, fmt.Errorf("creating info action: %w", err)
	}

	err = s.actionQueues.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		s.metrics.InfoRefreshFailed("enqueue")
		return common.Hash{}, fmt.Errorf("enqueueing info action: %w", err)
	}

	start := time.Now()
	response, err := s.responseStorage.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, timeout)
	s.metrics.ObserveNodeWait("info", time.Since(start), err)
	if err != nil {
		s.metrics.InfoRefreshFailed("wait_response")
		return common.Hash{}, fmt.Errorf("waiting for info response: %w", err)
	}
	if response.Result.Status != 1 {
		s.metrics.InfoRefreshFailed("action_status")
		return common.Hash{}, fmt.Errorf("TEE_INFO action failed: %s", response.Result.Log)
	}

	var result types.TeeInfoResponse

	err = json.Unmarshal(response.Result.Data, &result)
	if err != nil {
		s.metrics.InfoRefreshFailed("unmarshal")
		return common.Hash{}, fmt.Errorf("unmarshaling info response: %w", err)
	}

	if s.attestationCfg != nil {
		teeID, err := ParseTeeID(&result)
		if err != nil {
			s.metrics.InfoRefreshFailed("parse_tee_id")
			return common.Hash{}, fmt.Errorf("parsing tee ID: %w", err)
		}

		signingHash, err := signing.NewPayload(signing.TEEActionResult, result.TeeInfo.ChainID, [32]byte(response.Result.Hash())).Hash()
		if err != nil {
			s.metrics.InfoRefreshFailed("signing_hash")
			return common.Hash{}, fmt.Errorf("computing signing hash: %w", err)
		}

		err = utils.VerifySignature(signingHash[:], response.Signature, teeID)
		if err != nil {
			s.metrics.InfoRefreshFailed("verify_signature")
			return common.Hash{}, fmt.Errorf("verifying response signature: %w", err)
		}

		vErr := attestation.Verify(&result, challenge, s.attestationCfg)
		if s.attestationCfg.Enabled {
			res := "ok"
			if vErr != nil {
				res = "error"
			}
			s.metrics.AttestationVerified(res, attestation.Reason(vErr))
		}
		if vErr != nil {
			s.Lock()
			s.lastAttestationErr = vErr
			s.Unlock()
			s.metrics.InfoRefreshFailed("verify_attestation")
			return common.Hash{}, fmt.Errorf("verifying attestation: %w", vErr)
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

// ParseTeeID returns the TEE identity: the address derived from the TEE public key in the response.
func ParseTeeID(tir *types.TeeInfoResponse) (common.Address, error) {
	teePub, err := types.ParsePubKey(tir.TeeInfo.PublicKey)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*teePub), nil
}
