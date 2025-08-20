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
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/flare-foundation/tee-node/pkg/utils"
)

type Service struct {
	teeID common.Address

	vs       *Storage
	policies <-chan policy.SigningPolicy

	aq *queue.ActionQueues
	pk *ecdsa.PrivateKey
}

func NewService(teeID common.Address, pk *ecdsa.PrivateKey, policiesChan <-chan policy.SigningPolicy, aq *queue.ActionQueues, vs *Storage) Service {
	return Service{
		teeID:    teeID,
		vs:       vs,
		policies: policiesChan,
		aq:       aq,
		pk:       pk,
	}
}

func (s *Service) ServeInstruction(_ context.Context, i *instruction.Instruction) (*voting.SignedReceipt, error) {
	r, err := s.process(i)
	if err != nil {
		return nil, err
	}

	return r.Sign(s.pk)
}

func (s *Service) process(i *instruction.Instruction) (*voting.Receipt, error) {
	if i.Data.TeeID != s.teeID {
		return nil, fmt.Errorf("%w, wrong teeID", status.HTTP[400])
	}

	ok := op.IsValidPair(i.Data.OPType, i.Data.OPCommand)
	if !ok {
		return nil, fmt.Errorf("%w, invalid pair opType, opCommand ", status.HTTP[400])
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

// Forward listens to the out channel and enqueues received actions.
func (s *Service) Forward(ctx context.Context) error {
	// this can be done directly
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("instruction forwarding stopped %v", ctx.Err())
		case action := <-s.vs.Out:
			err := s.aq.Enqueue(ctx, action, queue.Main)
			if err != nil {
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
			return fmt.Errorf("listenToPolicies stopped %v", ctx.Err())
		case policy := <-s.policies:
			logger.Debugf("creating round for %d", policy.RewardEpochID)
			logger.Debugf("overwriting round for %d", policy.RewardEpochID-s.vs.Size())
			s.vs.CreateRound(&policy)
		}
	}
}

// Statuses returns the statuses for instructionID if it is present in the given rewardEpochID.
func (s *Service) Statuses(instructionID common.Hash, rewardEpochID uint32) (*voting.Statuses, error) {
	r, exists := s.vs.Get(rewardEpochID)
	if !exists {
		return nil, fmt.Errorf("%w: round not stored", status.HTTP[404])
	}

	boxes, exists := r.Voting.M[instructionID]
	if !exists {
		return nil, fmt.Errorf("%w: no instruction with the provided id", status.HTTP[404])
	}

	boxes.RLock()
	defer boxes.RUnlock()

	status := make([]voting.Status, 0, len(boxes.M))
	for hash := range boxes.M {
		s := boxes.M[hash].Status(hash)

		status = append(status, s)
	}

	return &voting.Statuses{
		InstructionID: instructionID,
		Status:        status,
	}, nil
}
