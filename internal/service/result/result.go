package result

import (
	"context"
	"errors"
	"fmt"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

type Service struct {
	rs *queue.ResponseStorage

	// A channel to trigger of wallet storage update
	WalletSync chan *types.ActionResult

	teeID common.Address
}

func NewService(rs *queue.ResponseStorage) Service {
	wst := make(chan *types.ActionResult, 10)

	return Service{rs: rs, WalletSync: wst}
}

func (s *Service) SetIdentity(teeID common.Address) error {
	if s.teeID.Cmp(common.Address{}) != 0 {
		return errors.New("address already set")
	}

	s.teeID = teeID
	return nil
}

// Serve returns response for actionID with tag "threshold" if present.
func (s *Service) Store(ctx context.Context, r *types.ActionResponse) error {
	if s.teeID.Cmp(common.Address{}) != 0 {
		signer, err := recoverSigner(r)
		if err != nil {
			return err
		}

		if signer.Cmp(s.teeID) != 0 {
			return fmt.Errorf("%w, invalid teeID", status.HTTP[403])
		}
	}

	if r.Result.Status {
		switch r.Result.OPCommand {
		case constants.KeyGenerate.Hash(), constants.KeyDataProviderRestore.Hash(), constants.KeyDelete.Hash():
			select {
			case s.WalletSync <- &r.Result:
			default:
				logger.Error("wallet synchronization channel full")
			}
		}
	}

	return s.rs.StoreResponse(ctx, r)
}

// Serve returns response for actionID with tag "threshold" if present.
func (s *Service) Serve(ctx context.Context, actionID common.Hash) (*types.ActionResponse, error) {
	return s.rs.GetResponse(ctx, actionID, types.Threshold)
}

// Serve returns response for actionID with tag "end" if present.
func (s *Service) ServeRewards(ctx context.Context, actionID common.Hash) (*types.ActionResponse, error) {
	return s.rs.GetResponse(ctx, actionID, types.End)
}

func recoverSigner(ar *types.ActionResponse) (common.Address, error) {
	hash := crypto.Keccak256(ar.Result.Data)
	hash = accounts.TextHash(hash)

	pub, err := crypto.SigToPub(hash, ar.Signature)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*pub), nil
}
