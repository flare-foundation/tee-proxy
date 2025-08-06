package instruction

import (
	"context"
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/tee"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
)

type Service struct {
	teeID common.Address

	vs       *voting.Storage
	policies <-chan policy.SigningPolicy

	aq *queue.ActionQueues
	pk *ecdsa.PrivateKey
}

func NewService(teeID common.Address, pk *ecdsa.PrivateKey, policiesChan <-chan policy.SigningPolicy, aq *queue.ActionQueues, vs *voting.Storage) Service {
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

	return voting.SignReceipt(s.pk, r)
}

func (s *Service) process(i *instruction.Instruction) (*tee.TeeStructsVoteReceipt, error) {
	if i.Data.TeeID != s.teeID {
		return nil, fmt.Errorf("%w, wrong teeID", status.HTTP[400])
	}

	ok := constants.IsValid(i.Data.OPType, i.Data.OPCommand)
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

func (s *Service) Forward(ctx context.Context) error {
	// this can be done directly
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("instructionforwarding stopped %v", ctx.Err())
		case box := <-s.vs.OutThreshold:
			box.RLock()
			a, err := box.Action(types.Threshold)
			box.RUnlock()
			if err != nil {
				continue
			}

			err = s.aq.Enqueue(ctx, a, queue.Main)
			if err != nil {
				continue
			}

		case box := <-s.vs.OutEnd:
			box.RLock()
			a, err := box.Action(types.End)
			box.RUnlock()
			if err != nil {
				continue
			}

			err = s.aq.Enqueue(ctx, a, queue.Main)
			if err != nil {
				continue
			}
			box.Lock()
			box.Delete()
			box.Unlock()
		}
	}
}

func (s *Service) ListenToPolicies(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("listenToPolicies stopped %v", ctx.Err())
		case policy := <-s.policies:
			s.vs.CreateRound(&policy)
		}
	}
}

func (s *Service) Status(instructionID common.Hash, rewardEpochID uint32) (*VoteStatus, error) {
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

	return &VoteStatus{
		InstructionID: instructionID,
		Status:        status,
	}, nil
}

type VoteStatus struct {
	InstructionID common.Hash     `json:"instructionId"`
	Status        []voting.Status `json:"status"`
}
