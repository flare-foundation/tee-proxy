package info

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flare-foundation/tee-proxy/pkg/config"

	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"

	"gorm.io/gorm"
)

// Service holds the latest TEE info and manages updating it.
//
// When you are accessing Latest or LastUpdate mutex should be used.
type Service struct {
	Latest      *types.TeeInfoResponse
	LastUpdated time.Time

	db *gorm.DB

	actionQueues    *queue.ActionQueues
	responseStorage *result.ResultStorage

	timingConfig *config.InfoTiming

	sync.RWMutex
}

func NewService(db *gorm.DB, aq *queue.ActionQueues, rs *result.ResultStorage, tc *config.InfoTiming) *Service {
	return &Service{
		Latest:      new(types.TeeInfoResponse),
		LastUpdated: time.Unix(0, 0),

		db:              db,
		actionQueues:    aq,
		responseStorage: rs,
		timingConfig:    tc,
	}
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
		err := s.updateInfo(ctx, s.timingConfig.CycleQueueResponseWait)
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

// FetchInfo updates info and returns the update.
func (s *Service) FetchInfo(ctx context.Context, timeout time.Duration) (*types.TeeInfoResponse, error) {
	err := s.updateInfo(ctx, timeout)
	if err != nil {
		return nil, err
	}

	return s.Latest, nil
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
func (s *Service) updateInfo(ctx context.Context, timeout time.Duration) error {
	block, err := database.FetchLatestBlock(ctx, s.db, nil)
	if err != nil {
		return fmt.Errorf("fetching latest block: %w", err)
	}

	action, err := newInfoAction(common.HexToHash(block.Hash))
	if err != nil {
		return fmt.Errorf("creating info action: %w", err)
	}

	err = s.actionQueues.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return fmt.Errorf("enqueueing info action: %w", err)
	}

	response, err := s.responseStorage.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, timeout)
	if err != nil {
		return fmt.Errorf("waiting for info response: %w", err)
	}
	if response.Result.Status != 1 {
		return fmt.Errorf("TEE_INFO action failed: %s", response.Result.Log)
	}

	var result types.TeeInfoResponse

	err = json.Unmarshal(response.Result.Data, &result)
	if err != nil {
		return fmt.Errorf("unmarshaling info response: %w", err)
	}

	s.Lock()
	defer s.Unlock()

	if s.Latest == nil {
		s.Latest = new(types.TeeInfoResponse)
	}
	*s.Latest = result
	s.LastUpdated = time.Now()

	return nil
}
