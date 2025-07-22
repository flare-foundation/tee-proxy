package result

import (
	"context"
	"errors"

	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
)

type Service struct {
	rs *queue.ResponseStorage

	// A channel to trigger synchronization of wallet storage.
	WalletSyncTrigger chan bool

	teeID common.Address
}

func NewService(rs *queue.ResponseStorage) Service {
	wst := make(chan bool, 1)

	return Service{rs: rs, WalletSyncTrigger: wst}
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
	switch r.Result.OPCommand {
	case constants.KeyGenerate.Hash(), constants.KeyDataProviderRestore.Hash():
		s.WalletSyncTrigger <- true
	}

	if s.teeID.Cmp(common.Address{}) != 0 || len(r.Signature) > 0 {
		signer, err := recoverSigner(r)
		if err != nil {
			return err
		}
		if signer.Cmp(s.teeID) != 0 {
			return errors.New("unknown tee id")
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
