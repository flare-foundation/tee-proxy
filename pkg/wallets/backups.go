package wallets

import (
	"context"
	"encoding/json"

	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-proxy/pkg/queue"

	"time"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
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

	return latestBackup.BackupID.RewardEpochID < activeEpochID
}

func (s *Storage) makeBackup(ctx context.Context, id IDPair) error {
	action, err := teeBackupAction(id)
	if err != nil {
		return err
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return err
	}

	result, err := s.rs.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, time.Minute)
	if err != nil {
		return err
	}

	err = s.createNewBackup(&result.Result)
	if err != nil {
		return err
	}

	return nil
}

func teeBackupAction(idPair IDPair) (*types.Action, error) {
	msg, err := json.Marshal(idPair)
	if err != nil {
		return nil, err
	}

	return queue.PrepareDirectAction(op.Get, op.TEEBackup, msg)
}

func (s *Storage) createNewBackup(r *types.ActionResult) error {
	var b *wallets.TEEBackupResponse
	err := json.Unmarshal(r.Data, &b)
	if err != nil {
		return err
	}

	idPair := IDPair{WalletID: b.BackupID.WalletID, KeyID: b.BackupID.KeyID}

	s.Backups[idPair] = b

	return nil
}
