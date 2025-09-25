package instruction

import (
	"fmt"
	"sync"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/storage"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/limiter"
	"github.com/flare-foundation/tee-proxy/pkg/meta"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
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

	config voting.Config

	// Metadata for the voting process.
	meta meta.Meta

	// Channel for actions created by voting.
	Out chan *types.Action
}

func NewStorage(config *voting.Config, size int, meta meta.Meta, outSize int) *Storage {
	out := make(chan *types.Action, outSize)

	config = config.SetDefault()

	return &Storage{
		Cyclic: storage.New[uint32, *Round](size),
		config: *config,
		meta:   meta,
		Out:    out,
	}
}

type Round struct {
	policy *policy.SigningPolicy
	Voting votingSync // instructionID -> instructionHash -> VoteBox

	limiter *limiter.Limiter

	sync.Mutex
}

type votingSync struct {
	M map[common.Hash]*voteBoxes

	sync.RWMutex
}

func newVoting() votingSync {
	return votingSync{
		M: map[common.Hash]*voteBoxes{},
	}
}

type voteBoxes struct {
	M map[common.Hash]*voteBox

	sync.RWMutex
}

func newVoteBoxes() *voteBoxes {
	return &voteBoxes{
		M: map[common.Hash]*voteBox{},
	}
}

// CreateRound creates round for signing policy and stores it.
// This in process overwrites an old round in the place of the new one.
// If a round with is already created nothing happens.
func (s *Storage) CreateRound(policy *policy.SigningPolicy) {
	_, exists := s.Get(policy.RewardEpochID)
	if exists {
		return
	}

	voters := make([]common.Address, 0, len(policy.Voters.VoterDataMap))
	for voter := range policy.Voters.VoterDataMap {
		voters = append(voters, voter)
	}

	limiter := limiter.New(voters, s.config.MaxPendingRequests)

	r := &Round{
		policy:  policy,
		Voting:  newVoting(),
		limiter: limiter,
	}

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
		defer round.Unlock()
		// we only save it at the end if no errors are returned
	} else {
		round.Unlock()
	}

	// Do not allow creating two boxes at once. Release lock if box exist.
	boxes.Lock()

	box, existsB := boxes.M[hash]
	if !existsB {
		box, err = s.startVoteBox(data, signer, round)
		defer boxes.Unlock()
		if err != nil {
			return nil, err
		}
		// we only save it at the end if no errors are returned
	} else {
		boxes.Unlock()
	}

	var vg voterGroup = 0
	weight := uint16(0)

	vd, exists := round.policy.Voters.VoterDataMap[signer]
	if exists {
		vg |= 0b01
		weight = vd.Weight
	}

	if box.proposal.cosigners[signer] {
		vg |= 0b10
	}

	box.Lock()
	defer box.Unlock()

	if box.deleted {
		return nil, fmt.Errorf("%w, voting already ended %s", status.HTTP[400], id.String())
	}

	receipt, finalized, err := box.addVote(signer, weight, signature, data.AdditionalVariableMessage, vg)
	if err != nil {
		return nil, fmt.Errorf("adding vote from %s to %v: %v", signer, id, err)
	}

	receipt.InstructionHash = hash

	// save box and schedule ending if it is newly created.
	if !existsB {
		box.iID = id
		box.iHash = hash
		boxes.M[hash] = box

		go func() {
			time.Sleep(time.Until(box.EndTime))

			box.Lock()
			defer box.Unlock()

			if box.Finalized {
				a, err := box.Action(types.End)
				if err != nil {
					logger.Errorf("failed crating end action for %v, %v: %v", id, hash, err)
				}
				s.Out <- a
			} else {
				logger.Debugf("closing non finalized box %v, %v", box.iID, box.iHash)
			}
			box.Delete()
		}()
	}

	// save boxes if they are newly created.
	if !existsBs {
		round.Voting.M[id] = boxes
	}

	if finalized {
		round.limiter.Decrement(box.Proposer)

		a, err := box.Action(types.Threshold)
		if err != nil {
			logger.Errorf("failed crating threshold action for %v, %v: %v", id, hash, err)
		}
		s.Out <- a
	}

	return &receipt, nil
}
