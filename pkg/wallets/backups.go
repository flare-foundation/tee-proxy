package wallets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"golang.org/x/sync/errgroup"

	"time"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
)

const expirationTime = 8 * 24 * time.Hour

func (s *Storage) MakeBackups(ctx context.Context) error {
	var eg errgroup.Group

	for key := range s.Keys {
		eg.Go(func() error { return s.makeBackup(ctx, key) })
	}

	return eg.Wait()
}

func (s *Storage) FetchBackup(ctx context.Context, idHash common.Hash) (*wallets.TEEBackupResponse, error) {
	return s.backups.Get(ctx, hex.EncodeToString(idHash[:]))
}

func (s *Storage) FetchLatestBackup(ctx context.Context, idPair IDPair) (*wallets.TEEBackupResponse, error) {
	idHash, err := s.index.Get(ctx, toKey(idPair))
	if err != nil {
		return nil, err
	}

	return s.backups.Get(ctx, hex.EncodeToString(idHash[:]))
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

	err = s.createNewBackup(ctx, &result.Result)
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

func (s *Storage) createNewBackup(ctx context.Context, r *types.ActionResult) error {
	var b *wallets.TEEBackupResponse
	err := json.Unmarshal(r.Data, &b)
	if err != nil {
		return err
	}

	idHash := b.BackupID.Hash()

	err = s.backups.SetWithTTL(ctx, hex.EncodeToString(idHash[:]), b, expirationTime)
	if err != nil {
		return err
	}

	idPair := IDPair{
		WalletID: b.BackupID.WalletID,
		KeyID:    b.BackupID.KeyID,
	}

	err = s.index.SetWithTTL(ctx, toKey(idPair), idHash, expirationTime)

	return err
}

func toKey(pair IDPair) string {
	return fmt.Sprintf("%s-%d", hex.EncodeToString(pair.WalletID[:]), pair.KeyID)
}
