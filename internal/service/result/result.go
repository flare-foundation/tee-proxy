package result

import (
	"context"
	"errors"
	"fmt"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

type Service struct {
	rs *ResultStorage

	// A channel to trigger of wallet storage update
	WalletSync    chan *types.ActionResult
	Backups       chan *types.ActionResult
	BackupTrigger chan bool

	teeID common.Address
}

func NewService(rs *ResultStorage) Service {
	wst := make(chan *types.ActionResult, 10)
	bst := make(chan *types.ActionResult, 20)

	btt := make(chan bool, 1)

	return Service{
		rs:            rs,
		WalletSync:    wst,
		Backups:       bst,
		BackupTrigger: btt}
}

func (s *Service) SetIdentity(teeID common.Address) error {
	if s.teeID.Cmp(common.Address{}) != 0 {
		return errors.New("address already set")
	}

	s.teeID = teeID

	return nil
}

// ProcessAndStore stores response into database and triggers appropriate hooks.
func (s *Service) ProcessAndStore(ctx context.Context, r *types.ActionResponse) error {
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
