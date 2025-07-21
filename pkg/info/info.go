package info

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

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

	actionQueues  *queue.ActionQueues
	resultStorage *queue.ResponseStorage

	sync.RWMutex
}

func NewStorage(db *gorm.DB, aq *queue.ActionQueues, rs *queue.ResponseStorage) Storage {
	return Storage{
		db:            db,
		actionQueues:  aq,
		resultStorage: rs,
	}
}

func (s *Storage) Run(ctx context.Context) error {
	errCount := 0

	ticker := time.NewTicker(time.Minute)

	for {
		<-ticker.C

		err := s.oneCycle(ctx)
		if err != nil {
			errCount++
		} else {
			errCount = 0
		}

		if errCount > 10 {
			logger.Error("neki")
		}
	}
}

func (s *Storage) InitialInfo(ctx context.Context) (*types.TeeInfoResponse, error) {
	err := s.oneCycle(ctx)
	if err != nil {
		return nil, err
	}

	return s.Latest, nil
}

func teeInfoAction(challenge common.Hash) (*types.Action, error) {
	m := types.TeeInfoRequest{
		Challenge: challenge,
	}

	msg, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	return queue.PrepareDirectAction(constants.Get, constants.TEEInfo, msg)
}

func (s *Storage) oneCycle(ctx context.Context) error {
	block, err := database.FetchLatestBlock(ctx, s.db, nil)
	if err != nil {
		return err
	}

	hash := common.HexToHash(block.Hash)
	action, err := teeInfoAction(hash)
	if err != nil {
		return err
	}

	err = s.actionQueues.Enqueue(ctx, action, queue.Read)
	if err != nil {
		return err
	}

	time.Sleep(10 * time.Second)

	result, err := s.waitOnResponse(ctx, 30*time.Second, action) // todo retry
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()

	s.Latest = result

	return nil
}

// move this to tools
func (s *Storage) waitOnResponse(ctx context.Context, timeout time.Duration, action *types.Action) (*types.TeeInfoResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var response *types.ActionResponse
	var err error
	for {
		if err := ctx.Err(); err != nil {
			cancel()
			return nil, err
		}

		response, err = s.resultStorage.GetResponse(ctx, action.Data.ID, action.Data.SubmissionTag) // todo retry
		if err == nil {
			break
		}

		time.Sleep(time.Second)
	}

	if !response.Result.Status {
		return nil, errors.New("action failed")
	}

	result := new(types.TeeInfoResponse)

	err = json.Unmarshal(response.Result.Data, result)
	if err != nil {
		return nil, err
	}

	return result, err
}
