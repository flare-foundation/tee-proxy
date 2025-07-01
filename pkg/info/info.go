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
	"github.com/flare-foundation/tee-proxy/pkg/redis"

	"github.com/flare-foundation/tee-node/pkg/types"

	"gorm.io/gorm"
)

type Storage struct {
	Latest *types.TeeInfoResponse

	db *gorm.DB

	actionStorage *redis.ActionStorage
	resultStorage *redis.ResponseStorage

	sync.RWMutex
}

func teeInfoAction(challenge common.Hash) (*types.Action, error) {
	m := types.TeeInfoRequest{
		Challenge: challenge,
	}

	mm, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	di := types.DirectInstruction{
		Data: types.DirectInstructionData{
			OPType:    constants.Get.Hash(),
			OPCommand: constants.TEEInfo.Hash(),
			Message:   mm,
		},
		Signatures: nil,
	}

	dim, err := json.Marshal(di)
	if err != nil {
		return nil, err
	}

	ad := types.ActionData{
		ID:   challenge,
		Type: types.Direct,

		SubmissionTag: types.Submit,
		Message:       dim,
	}

	return &types.Action{
		Data:                       ad,
		Signatures:                 nil,
		AdditionalVariableMessages: nil,
		Timestamps:                 nil,
		AdditionalActionData:       nil,
	}, nil
}

func (is *Storage) Run(ctx context.Context) error {
	errCount := 0

	ticker := time.NewTicker(time.Minute)

	for {
		<-ticker.C

		err := is.oneCycle(ctx)
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

func (is *Storage) oneCycle(ctx context.Context) error {
	block, err := database.FetchLatestBlock(ctx, is.db, nil)
	if err != nil {
		return err
	}

	hash := common.HexToHash(block.Hash)
	action, err := teeInfoAction(hash)
	if err != nil {
		return err
	}

	err = is.actionStorage.Enqueue(ctx, action, redis.Read)
	if err != nil {
		return err
	}

	time.Sleep(10 * time.Second)

	result, err := is.waitOnResponse(ctx, 30*time.Second, action) // todo retry
	if err != nil {
		return err
	}

	is.Lock()
	defer is.Unlock()

	is.Latest = result

	return nil
}

// move this to tools
func (is *Storage) waitOnResponse(ctx context.Context, timeout time.Duration, action *types.Action) (*types.TeeInfoResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var response *types.ActionResponse
	var err error
	for {
		if err := ctx.Err(); err != nil {
			cancel()
			return nil, err
		}

		response, err = is.resultStorage.GetResponse(ctx, action.Data.ID, action.Data.SubmissionTag) // todo retry
		if err == nil {
			break
		}

		time.Sleep(time.Second)
	}

	if !response.Status {
		return nil, errors.New("action failed")
	}

	result := new(types.TeeInfoResponse)

	err = json.Unmarshal(response.Result.ResultData.Message, result)
	if err != nil {
		return nil, err
	}

	return result, err
}
