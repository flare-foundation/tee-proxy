package instruction

import (
	"context"
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	pkgvoting "github.com/flare-foundation/tee-proxy/pkg/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/utils"
)

var (
	errWrongTeeID     = fmt.Errorf("%w: wrong teeID", status.HTTP[400])
	errInvalidOP      = fmt.Errorf("%w, invalid pair opType, opCommand ", status.HTTP[400])
	errRoundNotStored = fmt.Errorf("%w: round not stored", status.HTTP[404])
	errNoInstruction  = fmt.Errorf("%w: no instruction with the provided id", status.HTTP[404])
)

type Service struct {
	teeID common.Address

	vs *voting.Storage

	policies <-chan policy.SigningPolicy
	aq       *queue.ActionQueues
	privKey  *ecdsa.PrivateKey
}

func NewService(votingCfg *config.Voting, teeID common.Address, privKey *ecdsa.PrivateKey, policiesChan <-chan policy.SigningPolicy, aq *queue.ActionQueues, meta meta.Meta) Service {
	vs := voting.NewStorage(votingCfg, meta)

	return Service{
		teeID: teeID,
		vs:    vs,

		policies: policiesChan,
		aq:       aq,
		privKey:  privKey,
	}
}

// ServeInstruction accepts instruction, processes it, and returns the receipt.
//
// It checks that:
//   - correct teeID is set
//   - has a valid opType, opCommand pair
//
// Additional checks are done by the voting storage.
func (s *Service) ServeInstruction(_ context.Context, i *instruction.Instruction) (*pkgvoting.Receipt, error) {
	if i.Data.TeeID != s.teeID {
		return nil, errWrongTeeID
	}

	ok := op.IsValidPair(i.Data.OPType, i.Data.OPCommand)
	if !ok {
		return nil, errInvalidOP
	}

	hash, err := i.Data.HashForSigning()
	if err != nil {
		return nil, fmt.Errorf("hashing instruction %w", err)
	}

	signer, err := utils.SignatureToSignersAddress(hash[:], i.Signature)
	if err != nil {
		return nil, fmt.Errorf("retrieving signer %w", err)
	}

	return s.vs.AddVote(&i.Data, signer, i.Signature)
}

func (s *Service) Run(ctx context.Context) {
	go s.Forward(ctx)       //nolint:errcheck // todo
	s.ListenToPolicies(ctx) //nolint:errcheck // todo
}

// Forward listens to the out channel and enqueues received actions.
func (s *Service) Forward(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("instruction forwarding stopped %w", ctx.Err())
		case action := <-s.vs.Out:
			err := s.aq.Enqueue(ctx, action, processorutils.Main)
			if err != nil {
				logger.Errorf("enqueuing instruction action: %v", err)
				continue
			}
		}
	}
}

// ListenToPolicies listens to policy channel and creates a new round when a new policy arrives.
func (s *Service) ListenToPolicies(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("listenToPolicies stopped %w", ctx.Err())
		case policy := <-s.policies:
			logger.Debugf("creating round for %d", policy.RewardEpochID)
			logger.Debugf("overwriting round for %d", policy.RewardEpochID-s.vs.Size())
			s.vs.StoreNewRound(&policy)
		}
	}
}

// Statuses returns the statuses for instructionID if it is present in the given rewardEpochID.
func (s *Service) Statuses(instructionID common.Hash, rewardEpochID uint32) (*pkgvoting.Statuses, error) {
	r, exists := s.vs.Get(rewardEpochID)
	if !exists {
		return nil, errRoundNotStored
	}

	r.Voting.RLock()
	defer r.Voting.RUnlock()

	boxes, exists := r.Voting.M[instructionID]
	if !exists {
		return nil, errNoInstruction
	}

	boxes.RLock()
	defer boxes.RUnlock()

	status := make([]pkgvoting.Status, 0, len(boxes.M))
	for hash := range boxes.M {
		s := boxes.M[hash].Status(hash)

		status = append(status, s)
	}

	return &pkgvoting.Statuses{
		InstructionID: instructionID,
		FinalizedHash: boxes.FinalizedHash,
		Status:        status,
	}, nil
}
