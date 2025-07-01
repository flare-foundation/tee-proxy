package voting

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
}

type Receipt struct {
	InstructionHash               common.Hash `json:"instructionHash"`
	Sequence                      uint64      `json:"sequence"`
	AdditionalVariableMessageHash common.Hash `json:"additionalVariableMessageHash"`
	Timestamp                     uint64      `json:"timestamp"`
	VoteHash                      common.Hash `json:"voteHash"`
}

func (r *Receipt) Hash() common.Hash {
	return crypto.Keccak256Hash(
		r.InstructionHash[:],
		binary.BigEndian.AppendUint64(nil, r.Sequence),
		r.AdditionalVariableMessageHash[:],
		binary.BigEndian.AppendUint64(nil, r.Timestamp),
		r.VoteHash[:],
	)
}

func (m *Manager) ListenToPolicies(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("listenToPolicies stopped %v", ctx.Err())
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
		case box := <-m.votingStorage.outThreshold:
			box.RLock()
			a, err := box.action(types.Threshold)
			box.RUnlock()
			if err != nil {
				continue
			}

			err = m.actionStorage.Enqueue(ctx, a, redis.Main)
			if err != nil {
				continue
			}

		case box := <-m.votingStorage.outEnd:
			box.RLock()
			a, err := box.action(types.End)
			box.RUnlock()
			if err != nil {
				continue
			}

			err = m.actionStorage.Enqueue(ctx, a, redis.Main)
			if err != nil {
				continue
			}
			box.Lock()
			box.delete()
			box.Unlock()
		}
	}
}
