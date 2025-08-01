package info

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/flare-foundation/tee-proxy/pkg/config"

	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/tee-proxy/pkg/queue"

	"github.com/flare-foundation/tee-node/pkg/types"

	"gorm.io/gorm"
)

type Storage struct {
	Latest *types.TeeInfoResponse

	db *gorm.DB

	actionQueues    *queue.ActionQueues
	responseStorage *queue.ResponseStorage

	timingConfig *config.StorageTiming

	sync.RWMutex
}

func NewStorage(db *gorm.DB, aq *queue.ActionQueues, rs *queue.ResponseStorage, tc *config.StorageTiming) Storage {
	return Storage{
		db:              db,
		actionQueues:    aq,
		responseStorage: rs,
		timingConfig:    tc,
	}
}

func (s *Storage) Run(ctx context.Context) error {
	errCount := 0

	ticker := time.NewTicker(s.timingConfig.CycleInternal)

	for {
		<-ticker.C

		err := s.updateInfo(ctx)
		if err != nil {
			errCount++
		} else {
			errCount = 0
			logger.Info("info updated")
		}

		if errCount > 10 {
			logger.Error("neki")
		}
	}
}

// FetchInfo info updates info and returns the update.
func (s *Storage) FetchInfo(ctx context.Context) (*types.TeeInfoResponse, error) {
	err := s.updateInfo(ctx)
	if err != nil {
		return nil, err
	}
	logger.Info("info fetched")

	return s.Latest, nil
}

// action returns an action with opType GET, opCommand TEE_INFO,
// and challenge in tee info request.
func action(challenge common.Hash) (*types.Action, error) {
	m := types.TeeInfoRequest{
		Challenge: challenge,
	}

	msg, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	return queue.PrepareDirectAction(constants.Get, constants.TEEInfo, msg)
}

func (s *Storage) updateInfo(ctx context.Context) error {
	block, err := database.FetchLatestBlock(ctx, s.db, nil)
	if err != nil {
		return err
	}

	action, err := action(common.HexToHash(block.Hash))
	if err != nil {
		return err
	}

	err = s.actionQueues.Enqueue(ctx, action, queue.Read)
	if err != nil {
		return err
	}

	time.Sleep(s.timingConfig.CycleQueueResponseWait)

	response, err := s.responseStorage.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, 30*time.Second) // todo retry
	if err != nil {
		return err
	}
	if !response.Result.Status {
		return errors.New("action failed")
	}

	result := new(types.TeeInfoResponse)

	err = json.Unmarshal(response.Result.Data, result)
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()

	s.Latest = result

	return nil
}
