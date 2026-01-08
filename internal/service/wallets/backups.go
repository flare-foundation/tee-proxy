package wallets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"time"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
)

const expirationTime = 8 * 24 * time.Hour

// InitiateBackups triggers TEE_BACKUP action for all stored keys.
func (s *Service) InitiateBackups(ctx context.Context) error {
	var eg errgroup.Group

	s.RLock()
	defer s.RUnlock()

	for key := range s.Keys {
		// make sure the key id is not edited by the time of execution
		f := func() error {
			err := s.initiateBackup(ctx, key)
			if err != nil {
				return fmt.Errorf("initiating backup for key %v: %w", key, err)
			}
			return nil
		}
		eg.Go(f)
	}

	return eg.Wait()
}

// FetchBackup fetches backup for backupIDHash
func (s *Service) FetchBackup(ctx context.Context, idHash common.Hash) (*wallets.TEEBackupResponse, error) {
	b, err := s.backups.Get(ctx, hex.EncodeToString(idHash[:]))
	if err != nil {
		rErr := fmt.Errorf("fetching backup data with hash %s: %w", idHash.Hex(), err)
		if errors.Is(err, redis.Nil) {
			rErr = status.Add(err, 404)
		}
		return nil, rErr
	}

	return b, nil
}

// FetchLatestBackup fetches latest backup for id pair.
func (s *Service) FetchLatestBackup(ctx context.Context, idPair IDPair) (*wallets.TEEBackupResponse, error) {
	idHash, err := s.index.Get(ctx, toKey(idPair))
	if err != nil {
		rErr := fmt.Errorf("fetching backup id hash for %v: %w", idPair, err)
		if errors.Is(err, redis.Nil) {
			rErr = status.Add(err, 404)
		}
		return nil, rErr
	}

	backup, err := s.backups.Get(ctx, hex.EncodeToString(idHash[:]))
	if err != nil {
		return nil, fmt.Errorf("fetching latest backup data for %v with hash %s: %w", idPair, idHash.Hex(), err)
	}

	return backup, nil
}

func (s *Service) initiateBackup(ctx context.Context, id IDPair) error {
	action, err := teeBackupAction(id)
	if err != nil {
		return err
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Backup)
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

func (s *Service) createNewBackup(ctx context.Context, r *types.ActionResult) error {
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
