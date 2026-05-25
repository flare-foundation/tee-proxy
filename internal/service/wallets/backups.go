package wallets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"golang.org/x/sync/errgroup"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
)

// initiateBackupsConcurrency caps concurrent TEE_BACKUP enqueues at epoch rollover.
const initiateBackupsConcurrency = 10

// backupStoreTimeout caps each storage call in createNewBackup. Bounded so a degraded
// Redis can't stall the wallet event loop indefinitely.
const backupStoreTimeout = 10 * time.Second

// InitiateBackups triggers TEE_BACKUP action for all stored keys.
func (s *Service) InitiateBackups(ctx context.Context) error {
	s.RLock()
	ids := make([]IDPair, 0, len(s.Keys))
	for id := range s.Keys {
		ids = append(ids, id)
	}
	s.RUnlock()

	var eg errgroup.Group
	eg.SetLimit(initiateBackupsConcurrency)
	for _, id := range ids {
		eg.Go(func() error {
			err := s.initiateBackup(ctx, id)
			if err != nil {
				return fmt.Errorf("initiating backup for key %v: %w", id, err)
			}
			return nil
		})
	}

	return eg.Wait()
}

// FetchBackup fetches backup for backupIDHash
func (s *Service) FetchBackup(ctx context.Context, idHash common.Hash) (*wallets.TEEBackupResponse, error) {
	b, err := s.backups.Get(ctx, hex.EncodeToString(idHash[:]))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Add(err, 404)
		}
		return nil, fmt.Errorf("fetching backup data with hash %s: %w", idHash.Hex(), err)
	}

	return b, nil
}

// FetchLatestBackup fetches latest backup for id pair.
func (s *Service) FetchLatestBackup(ctx context.Context, idPair IDPair) (*wallets.TEEBackupResponse, error) {
	idHash, err := s.index.Get(ctx, toKey(idPair))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Add(err, 404)
		}
		return nil, fmt.Errorf("fetching backup id hash for %v: %w", idPair, err)
	}

	backup, err := s.backups.Get(ctx, hex.EncodeToString(idHash[:]))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			logger.Warnf("orphan index for %v points at missing body %s", idPair, idHash.Hex())
			return nil, status.Add(err, 404)
		}
		return nil, fmt.Errorf("fetching latest backup data for %v with hash %s: %w", idPair, idHash.Hex(), err)
	}

	return backup, nil
}

func (s *Service) initiateBackup(ctx context.Context, id IDPair) error {
	action, err := teeBackupAction(id)
	if err != nil {
		return fmt.Errorf("creating backup action: %w", err)
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Backup)
	if err != nil {
		return fmt.Errorf("enqueueing backup action: %w", err)
	}

	return nil
}

func teeBackupAction(idPair IDPair) (*types.Action, error) {
	msg, err := json.Marshal(idPair)
	if err != nil {
		return nil, fmt.Errorf("marshaling backup ID pair: %w", err)
	}

	return queue.PrepareDirectAction(op.Get, op.TEEBackup, msg)
}

func (s *Service) createNewBackup(ctx context.Context, r *types.ActionResult) error {
	var b *wallets.TEEBackupResponse
	err := json.Unmarshal(r.Data, &b)
	if err != nil {
		return fmt.Errorf("unmarshaling backup response: %w", err)
	}
	if b == nil {
		return errors.New("backup response is nil")
	}

	idHash, err := b.BackupID.Hash()
	if err != nil {
		return fmt.Errorf("hashing backup ID: %w", err)
	}
	idHashKey := hex.EncodeToString(idHash[:])

	bodyCtx, bodyCancel := context.WithTimeout(ctx, backupStoreTimeout)
	defer bodyCancel()
	err = s.backups.SetWithTTL(bodyCtx, idHashKey, b, s.backupTTL)
	if err != nil {
		return fmt.Errorf("storing backup: %w", err)
	}

	idPair := IDPair{
		WalletID: b.BackupID.WalletID,
		KeyID:    b.BackupID.KeyID,
	}

	indexCtx, indexCancel := context.WithTimeout(ctx, backupStoreTimeout)
	defer indexCancel()
	err = s.index.SetWithTTL(indexCtx, toKey(idPair), idHash, s.backupTTL)
	if err != nil {
		// Body landed but index didn't: backup is orphaned until backupTTL.
		logger.Errorf("backup orphaned for %v: body at %s but index write failed: %v", idPair, idHashKey, err)
		return fmt.Errorf("storing backup index: %w", err)
	}

	return nil
}

func toKey(pair IDPair) string {
	return fmt.Sprintf("%s-%d", hex.EncodeToString(pair.WalletID[:]), pair.KeyID)
}
