package voting

import (
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

// Storage uses a cyclic storage that host voting processes in rounds - one round for each signingPolicy.
// Round for signing policy ID is stored at ID%size. When a new round is created, the old one at its place is overwritten.
//
// Meta provides the meta data for voting processes.
type Storage struct {
	*storage.Cyclic[uint32, *Round]

	config config.Voting

	// Metadata for the voting process.
	meta meta.Meta

	// Channel for actions created by voting.
	Out chan *types.Action
}

func NewStorage(config *config.Voting, size int, meta meta.Meta, outSize int) *Storage {
	out := make(chan *types.Action, outSize)

	config = config.SetDefault()

	return &Storage{
		Cyclic: storage.New[uint32, *Round](size),
		config: *config,
		meta:   meta,
		Out:    out,
	}
}

// StoreNewRound creates a round for signing policy and stores it.
// This in process overwrites an old round in the place of the new one.
// If a round is already created nothing happens.
func (s *Storage) StoreNewRound(policy *policy.SigningPolicy) {
	_, exists := s.Get(policy.RewardEpochID)
	if exists {
		return
	}

	r := createRound(policy, s.config.MaxPendingRequests)

	s.Store(policy.RewardEpochID, r)
}

// AddVote adds vote to a correct vote box in a correct round and returns a receipt.
// If a round does not exits, an error is returned.
// If a VoteBox does not exist yet, a new VoteBox is created if the proposer is not limited.
func (s *Storage) AddVote(data *instruction.Data, signer common.Address, signature []byte) (*voting.Receipt, error) {
	id := data.InstructionID

	err := checkSize(data)
	if err != nil {
		return nil, err
	}

	hash, err := data.HashFixed()
	if err != nil {
		return nil, err
	}

	round, exists := s.Get(data.RewardEpochID)
	if !exists {
		return nil, fmt.Errorf("%w no round %d", status.HTTP[404], data.RewardEpochID)
	}

	err = s.meta.CheckConsistency(data, signer)
	if err != nil {
		return nil, fmt.Errorf("verifying message validity: %w", err)
	}

	// Do not allow creating two sets of boxes at once. Release lock if set of boxes exists.
	round.Lock()
	boxes, existsBs := round.Voting.M[id]
	if !existsBs {
		boxes = newVoteBoxes()
		defer round.Unlock() // we only save it at the end if no errors are returned
	} else {
		round.Unlock()
	}

	// Do not allow creating working with two boxes at once
	boxes.Lock()
	defer boxes.Unlock()

	box, existsB := boxes.M[hash]
	if !existsB {
		box, err = startVoteBox(data, signer, round, s.meta, s.config.ProposalExpiration)
		if err != nil {
			return nil, err
		}
	}

	vg, weight := voterGroupCheck(signer, round.policy.Voters.VoterDataMap, box.proposal.cosigners)

	box.Lock()
	defer box.Unlock()

	if box.deleted {
		return nil, fmt.Errorf("%w, voting already ended %s", status.HTTP[400], id.String())
	}

	receipt, finalized, err := box.addVote(signer, weight, signature, data.AdditionalVariableMessage, vg)
	if err != nil {
		return nil, fmt.Errorf("adding vote from %s to %v: %v", signer, id, err)
	}

	// if box is newly created, save it and scheduleEnd.
	if !existsB {
		boxes.M[hash] = box
		go box.scheduleEnd(s.Out, data.OPType, data.OPCommand)
	}

	// if boxes are newly created save them
	if !existsBs {
		round.Voting.M[id] = boxes
	}

	if finalized {
		round.limiter.Decrement(box.Proposer)

		switch {
		case data.OPType == op.Wallet.Hash() && data.OPCommand == op.KeyDataProviderRestore.Hash():
			// only sent "threshold" action at the end of voting if finalized
		default:
			if boxes.FinalizedHash.Cmp(common.Hash{}) == 0 {
				boxes.FinalizedHash = hash
			} else if boxes.FinalizedHash.Cmp(hash) != 0 {
				logger.Infof("instruction id %v already finalized with %v also reached threshold with %v", box.iID, boxes.FinalizedHash, hash)
			}

			a, err := box.Action(types.Threshold)
			if err != nil {
				logger.Errorf("failed crating threshold action for %v, %v: %v", id, hash, err)
			} else {
				s.Out <- a
			}
		}
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
