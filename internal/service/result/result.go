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
	keyActionsChanSize    = 1000
	backupsChanSize       = 1000
	backupTriggerChanSize = 1
)

var (
	errAddressAlreadySet = errors.New("address already set")
	errInvalidTeeID      = fmt.Errorf("%w: invalid teeID", status.HTTP[403])
	// errBootstrapNotTeeInfo rejects non-TEE_INFO responses arriving before SetIdentity.
	errBootstrapNotTeeInfo = fmt.Errorf("%w: expected TEE_INFO response before identity is set", status.HTTP[403])
)

// Service handles processing and storage of TEE action results.
type Service struct {
	rs *ResultStorage

	// A channel for key update actions (KEY_GENERATE, KEY_DATA_PROVIDER_RESTORE,
	// KEY_DIRECT_RESTORE, KEY_DELETE)
	KeyActions chan *types.ActionResult
	// A channel for backup actions (TEE_BACKUP)
	Backups chan *types.ActionResult
	// A channel for backup trigger actions (UPDATE_POLICY)
	BackupTrigger chan bool

	mu    sync.RWMutex
	teeID common.Address

	// storageMu guards lastStorageErr independently of mu so storage outcomes
	// can be recorded without blocking the RLock held during ProcessAndStore.
	storageMu      sync.RWMutex
	lastStorageErr error
}

// NewService creates a new result service.
func NewService(rs *ResultStorage) *Service {
	kat := make(chan *types.ActionResult, keyActionsChanSize)
	bst := make(chan *types.ActionResult, backupsChanSize)
	btt := make(chan bool, backupTriggerChanSize)

	return &Service{
		rs:            rs,
		KeyActions:    kat,
		Backups:       bst,
		BackupTrigger: btt,
	}
}

// SetIdentity sets the TEE identity for the service. It can only be set once.
func (s *Service) SetIdentity(teeID common.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.teeID.Cmp(common.Address{}) != 0 {
		return errAddressAlreadySet
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
			logger.Errorf("recover signer for result %s: %v, result log: %s", r.Result.ID, err, r.Result.Log)
			return fmt.Errorf("recovering signer: %w", err)
		}

		if signer.Cmp(s.teeID) != 0 {
			return errInvalidTeeID
		}
	} else if r.Result.OPCommand != op.TEEInfo.Hash() {
		// Pre-SetIdentity: signatures cannot be verified, so only TEE_INFO is accepted.
		return errBootstrapNotTeeInfo
	}

	if r.Result.Status == 0 {
		logger.Errorf("received failed result %s, tag %s, opType %s, opCommand %s, log: %s",
			r.Result.ID, r.Result.SubmissionTag, op.HashToOPType(r.Result.OPType), op.HashToOPCommand(r.Result.OPCommand), r.Result.Log)
	} else {
		logger.Debugf("received result %s, tag %s, opType %s, opCommand %s, status %d",
			r.Result.ID, r.Result.SubmissionTag, op.HashToOPType(r.Result.OPType), op.HashToOPCommand(r.Result.OPCommand), r.Result.Status)
	}

	if r.Result.Status == 1 && r.Result.SubmissionTag != types.End {
		switch r.Result.OPCommand {
		case op.KeyGenerate.Hash(), op.KeyDataProviderRestore.Hash(), op.KeyDirectRestore.Hash(), op.KeyDelete.Hash():
			select {
			case s.KeyActions <- &r.Result:
			default:
				logger.Error("key actions channel full")
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

	err := s.rs.StoreResponse(ctx, r)
	s.recordStorageResult(err)
	return err
}

// LastStorageErr returns the most recent StoreResponse error, or nil if the last attempt succeeded.
// Auto-recovers: cleared by the next successful store.
func (s *Service) LastStorageErr() error {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	return s.lastStorageErr
}

func (s *Service) recordStorageResult(err error) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	s.lastStorageErr = err
}

// Serve returns response for actionID with provided submissionTag if present.
func (s *Service) Serve(ctx context.Context, actionID common.Hash, submissionTag types.SubmissionTag) (*types.ActionResponse, error) {
	return s.rs.FetchResponse(ctx, actionID, submissionTag)
}

func recoverSigner(ar *types.ActionResponse) (common.Address, error) {
	hash := accounts.TextHash((&ar.Result).Hash())

	pub, err := crypto.SigToPub(hash, ar.Signature)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*pub), nil
}
