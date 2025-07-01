package wallets

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"

	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/tee-proxy/pkg/queue"

	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/flare-foundation/tee-node/pkg/types"
)

func (s *Storage) MakeBackups(ctx context.Context, epochID uint32) error {
	// todo clear old keys

	for key := range s.Keys {
		create := s.shouldCreateNewBackup(key, epochID)
		if create {
			err := s.makeBackup(ctx, key)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Storage) shouldCreateNewBackup(id IDPair, activeEpochID uint32) bool {
	latestBackup, exists := s.Backups[id]
	if !exists {
		return true
	}

	return latestBackup.BackupId.RewardEpochID < activeEpochID
}

func (s *Storage) makeBackup(ctx context.Context, id IDPair) error {
	actionID := common.Hash{}
	_, err := io.ReadFull(rand.Reader, actionID[:])
	if err != nil {
		return err
	}

	action, err := teeBackupAction(actionID, id)
	if err != nil {
		return err
	}

	err = s.as.Enqueue(ctx, action, queue.Read)
	if err != nil {
		return err
	}

	result, err := s.waitOnResult(ctx, time.Minute, action)
	if err != nil {
		return err
	}

	err = s.createNewBackup(result)
	if err != nil {
		return err
	}

	return nil
}

func teeBackupAction(actionID common.Hash, idPair IDPair) (*types.Action, error) {
	msg, err := json.Marshal(idPair)
	if err != nil {
		return nil, err
	}

	di := types.DirectInstruction{
		Data: types.DirectInstructionData{
			OPType:    constants.Get.Hash(),
			OPCommand: constants.TEEBackup.Hash(),
			Message:   msg,
		},
		Signatures: nil,
	}

	dmsg, err := json.Marshal(di)
	if err != nil {
		return nil, err
	}

	ad := types.ActionData{
		ID:            actionID,
		Type:          types.Direct,
		SubmissionTag: types.Submit,
		Message:       dmsg,
	}

	return &types.Action{
		Data:                       ad,
		Signatures:                 nil,
		AdditionalVariableMessages: nil,
		Timestamps:                 nil,
		AdditionalActionData:       []byte{},
	}, nil
}

func (s *Storage) createNewBackup(r *types.ActionResponse) error {
	var b *types.WalletGetBackupResponse
	err := json.Unmarshal(r.Result.ResultData.Message, &b)
	if err != nil {
		return err
	}

	idPair := IDPair{WalletID: b.BackupId.WalletId, KeyID: b.BackupId.KeyId}

	s.Backups[idPair] = b

	return nil
}
