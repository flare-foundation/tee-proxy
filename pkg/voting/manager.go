package voting

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/redis"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-node/pkg/utils"
)

type Manager struct {
	votingStorage *Storage
	policies      chan policy.SigningPolicy

	actionStorage *redis.ActionStorage

	instructions chan *types.Action
}

type Receipt struct {
	InstructionHash               common.Hash `json:"instructionHash"`
	Sequence                      uint64      `json:"sequence"`
	AdditionalVariableMessageHash common.Hash `json:"additionalVariableMessageHash"`
	Timestamp                     uint64      `json:"timestamp"`
	VoteHash                      common.Hash `json:"voteHash"`
}

func (m *Manager) ListenToPolicies(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("listenToPolicies  stopped %v", ctx.Err())
		case policy := <-m.policies:
			m.votingStorage.CreateRound(&policy)
		}
	}
}

// TODO rename
func (m *Manager) Process(i *instruction.Instruction) (*Receipt, error) {
	hash, err := i.Data.HashForSigning()
	if err != nil {
		return nil, fmt.Errorf("processing instruction %v", err)
	}

	signer, err := utils.SignatureToSignersAddress(hash[:], i.Signature)
	if err != nil {
		return nil, fmt.Errorf("retrieving signer %v", err)
	}

	return m.votingStorage.AddVote(&i.Data, signer, i.Signature)
}

func (m *Manager) Forward(ctx context.Context) error {
	// this can be done directly
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("forwarding context stopped %v", ctx.Err())
		case action := <-m.instructions:

			err := m.actionStorage.Enqueue(ctx, action, redis.Main)
			logger.Errorf("enqueuing action %v", err) // TODO handle this
		}
	}
}
