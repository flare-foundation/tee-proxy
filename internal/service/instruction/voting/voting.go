package voting

import (
	"context"
	"fmt"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/storage"
	"github.com/flare-foundation/go-flare-common/pkg/voters"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
)

const maxBIPS = 10000

type voterGroup uint8

const (
	invalidVoter voterGroup = 0
	provider     voterGroup = 0b01
	cosigner     voterGroup = 0b10
	both         voterGroup = 0b11
)

// isCosigner member from a voter group is a cosigner.
func (vg voterGroup) isCosigner() bool {
	return vg&cosigner != 0
}

// Storage holds one voting Round per signing policy in a cyclic buffer; storing a new round at
// ID%size overwrites the previous occupant.
type Storage struct {
	*storage.Cyclic[uint32, *Round]

	config config.Voting

	// Metadata for the voting process.
	meta meta.Meta

	// Channel for actions created by voting.
	Out chan *types.Action

	// Service-lifetime ctx; per-box goroutines outlive the request that spawns them.
	ctx context.Context //nolint:containedctx // service-lifetime ctx, see comment
}

// NewStorage creates a voting Storage backed by a cyclic buffer sized by config.HistorySize.
// ctx is the service-lifetime context that bounds per-box goroutines created during AddVote.
func NewStorage(ctx context.Context, config *config.Voting, meta meta.Meta) *Storage {
	out := make(chan *types.Action, config.FinalizedBufferSize)

	config = config.SetDefault()

	return &Storage{
		Cyclic: storage.New[uint32, *Round](config.HistorySize),
		config: *config,
		meta:   meta,
		Out:    out,
		ctx:    ctx,
	}
}

// StoreNewRound creates and stores a round for the policy, overwriting the previous occupant
// at the same cyclic slot. A no-op if a round for this policy already exists.
func (s *Storage) StoreNewRound(policy *policy.SigningPolicy) {
	_, exists := s.Get(policy.RewardEpochID)
	if exists {
		return
	}

	r := createRound(policy, s.config.MaxPendingRequests)

	s.Store(policy.RewardEpochID, r)
}

// AddVote records a vote in the appropriate box and returns a signed receipt.
// Returns an error if the round is missing; opens a new VoteBox for an unseen instruction
// hash when the proposer is within the limiter's per-voter cap.
func (s *Storage) AddVote(data *instruction.Data, signer common.Address, signature []byte) (*voting.Receipt, error) {
	id := data.InstructionID

	err := checkSize(data, s.config.MaxProviderVote)
	if err != nil {
		return nil, err
	}

	hash, err := data.HashFixed()
	if err != nil {
		return nil, err
	}

	round, exists := s.Get(data.RewardEpochID)
	if !exists {
		return nil, fmt.Errorf("%w: no round %d", status.HTTP[404], data.RewardEpochID)
	}

	err = s.meta.CheckConsistency(data, signer)
	if err != nil {
		return nil, fmt.Errorf("%w: inconsistent data: %v", status.HTTP[400], err)
	}

	var receipt voting.Receipt
	var actionToSend *types.Action

	// Hold locks only inside this closure; the channel send below stays unlocked.
	err = func() error {
		round.Voting.Lock()
		boxes, existsBs := round.Voting.M[id]
		if !existsBs {
			boxes = newVoteBoxes()
			defer round.Voting.Unlock() // we only save it at the end if no errors are returned
		} else {
			round.Voting.Unlock()
		}

		boxes.Lock()
		defer boxes.Unlock()

		box, existsB := boxes.M[hash]
		if !existsB {
			var err error
			box, err = startVoteBox(data, signer, round, s.meta, s.config.ProposalExpiration)
			if err != nil {
				return err
			}
		}

		box.Lock()
		defer box.Unlock()

		if box.deleted {
			return fmt.Errorf("%w: %s", errVotingEnded, id)
		}

		// box.proposal.cosigners is read under box.Lock because scheduleEnd's
		// deferred delete() writes it under the same lock. Without this ordering,
		// a second AddVote arriving as the box expires races with delete().
		vg, weight := voterGroupCheck(signer, round.policy.Voters.VoterDataMap, box.proposal.cosigners)

		r, finalized, err := box.addVote(signer, weight, signature, data.AdditionalVariableMessage, vg)
		if err != nil {
			return fmt.Errorf("adding vote from %s to %v: %w", signer, id, err)
		}
		receipt = r

		if !existsB {
			boxes.M[hash] = box
			go box.scheduleEnd(s.ctx, s.Out, boxes)
		}

		if !existsBs {
			round.Voting.M[id] = boxes
		}

		if finalized {
			round.limiter.Decrement(box.Proposer)

			switch {
			case data.OPType == op.Wallet.Hash() && data.OPCommand == op.KeyDataProviderRestore.Hash():
				// only sent "threshold" action at the end of voting if finalized
			default:
				// Two boxes with the same instructionID can each finalize; dedup happens downstream.
				if boxes.FinalizedHash.Cmp(common.Hash{}) == 0 {
					boxes.FinalizedHash = hash
				} else if boxes.FinalizedHash.Cmp(hash) != 0 {
					logger.Warnf("instruction id %v already finalized with %v, emitting additional threshold action for %v", box.iID, boxes.FinalizedHash, hash)
				}

				a, err := box.Action(types.Threshold)
				if err != nil {
					logger.Errorf("failed crating threshold action for %v, %v: %v", id, hash, err)
				} else {
					actionToSend = a
				}
			}
		}

		return nil
	}()

	if err != nil {
		return nil, err
	}

	if actionToSend != nil {
		s.Out <- actionToSend
	}

	return &receipt, nil
}

func voterGroupCheck(signer common.Address, voterDataMap map[common.Address]voters.VoterData, cosigners map[common.Address]bool) (voterGroup, uint16) {
	var vg voterGroup = 0
	weight := uint16(0)

	vd, exists := voterDataMap[signer]
	if exists {
		vg |= provider
		weight = vd.Weight
	}

	if cosigners[signer] {
		vg |= cosigner
	}

	return vg, weight
}
