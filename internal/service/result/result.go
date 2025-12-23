package result

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

const (
	walletSyncChanSize    = 10
	backupsChanSize       = 20
	backupTriggerChanSize = 1
)

// Service handles processing and storage of TEE action results.
type Service struct {
	rs *ResultStorage

	// A channel for wallet update actions (KEY_GENERATE, KEY_DATA_PROVIDER_RESTORE, KEY_DELETE)
	WalletSync chan *types.ActionResult
	// A channel for backup actions (TEE_BACKUP)
	Backups chan *types.ActionResult
	// A channel for backup trigger actions (UPDATE_POLICY)
	BackupTrigger chan bool

	mu    sync.RWMutex
	teeID common.Address
}

// NewService creates a new result service.
func NewService(rs *ResultStorage) *Service {
	wst := make(chan *types.ActionResult, walletSyncChanSize)
	bst := make(chan *types.ActionResult, backupsChanSize)

	btt := make(chan bool, backupTriggerChanSize)

	return &Service{
		rs:            rs,
		WalletSync:    wst,
		Backups:       bst,
		BackupTrigger: btt,
	}
}

// SetIdentity sets the TEE identity for the service. It can only be set once.
func (s *Service) SetIdentity(teeID common.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.teeID.Cmp(common.Address{}) != 0 {
		return errors.New("address already set")
	}

	s.teeID = teeID

	return nil
}

// ProcessAndStore stores response into database and triggers appropriate hooks.
func (s *Service) ProcessAndStore(ctx context.Context, r *types.ActionResponse) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.teeID.Cmp(common.Address{}) != 0 {
		signer, err := recoverSigner(r)
		if err != nil {
			return err
		}

		if signer.Cmp(s.teeID) != 0 {
			return fmt.Errorf("%w, invalid teeID", status.HTTP[403])
		}
	}

	if r.Result.Status == 1 && r.Result.SubmissionTag != types.End {
		switch r.Result.OPCommand {
		case op.KeyGenerate.Hash(), op.KeyDataProviderRestore.Hash(), op.KeyDelete.Hash():
			select {
			case s.WalletSync <- &r.Result:
			default:
				logger.Error("wallet synchronization channel full")
			}
		case op.TEEBackup.Hash():
			select {
			case s.Backups <- &r.Result:
			default:
				logger.Error("backup storage channel full")
			}
		case op.UpdatePolicy.Hash():
			select {
			case s.BackupTrigger <- true:
			default:
				logger.Error("backup trigger channel full")
			}
		}
	}

	return s.rs.StoreResponse(ctx, r)
}

// Serve returns response for actionID with provided submissionTag if present.
func (s *Service) Serve(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	return s.rs.GetResponse(ctx, actionID, submissionTag)
}

func recoverSigner(ar *types.ActionResponse) (common.Address, error) {
	hash := accounts.TextHash(crypto.Keccak256(ar.Result.Data))

	pub, err := crypto.SigToPub(hash, ar.Signature)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*pub), nil
}
