package instruction

import (
	"context"
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
)

type Service struct {
	teeID common.Address

	vs       *voting.Storage
	policies chan policy.SigningPolicy

	as *queue.ActionQueues
	pk *ecdsa.PrivateKey
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

	hash, err := i.Data.HashForSigning()
	if err != nil {
		return nil, fmt.Errorf("processing instruction %v", err)
	}

	signer, err := utils.SignatureToSignersAddress(hash[:], i.Signature)
	if err != nil {
		return nil, fmt.Errorf("retrieving signer %v", err)
	}

	return s.vs.AddVote(&i.Data, signer, i.Signature)
}

func (s *Service) Forward(ctx context.Context) error {
	// this can be done directly
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("forwarding context stopped %v", ctx.Err())
		case box := <-s.vs.OutThreshold:
			box.RLock()
			a, err := box.Action(types.Threshold)
			box.RUnlock()
			if err != nil {
				continue
			}

			err = s.as.Enqueue(ctx, a, queue.Main)
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

			err = s.as.Enqueue(ctx, a, queue.Main)
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
