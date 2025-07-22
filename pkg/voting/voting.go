package voting

import (
	"fmt"
	"sync"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/storage"
	"github.com/flare-foundation/tee-proxy/pkg/limiter"
	"github.com/flare-foundation/tee-proxy/pkg/meta"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
)

const maxBIPS = 10000

const proposalExpiration = 5 * time.Second // in production 120 , make this configurable

const maxPendingRequests = 100 // MORE??

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

	// Metadata for the voting process.
	meta meta.Meta

	// Channel for boxes that reach threshold.
	OutThreshold chan *voteBox
	// Channel for boxes that reach the end of voting.
	OutEnd chan *voteBox
}

func NewStorage(size int, meta meta.Meta, outSize int) *Storage {
	outThreshold := make(chan *voteBox, outSize)
	outEnd := make(chan *voteBox, outSize)

	return &Storage{storage.New[uint32, *Round](size), meta, outThreshold, outEnd}
}

type Round struct {
	policy *policy.SigningPolicy
	Voting voting // instructionID -> instructionHash -> VoteBox

	limiter *limiter.Limiter

	sync.Mutex
}

type voting struct {
	M map[common.Hash]*voteBoxes

	sync.RWMutex
}

func newVoting() voting {
	return voting{
		M: map[common.Hash]*voteBoxes{},
	}
}

type voteBoxes struct {
	M map[common.Hash]*voteBox

	sync.RWMutex
}

func newVotBoxes() *voteBoxes {
	return &voteBoxes{
		M: map[common.Hash]*voteBox{},
	}
}

// CreateRound creates round for signing policy and stores it.
// This in process overwrites an old round.
func (vs *Storage) CreateRound(policy *policy.SigningPolicy) {
	voters := make([]common.Address, 0, len(policy.Voters.VoterDataMap)) // todo: make this nicer
	for voter := range policy.Voters.VoterDataMap {
		voters = append(voters, voter)
	}

	limiter := limiter.New(voters, maxPendingRequests)

	r := &Round{
		policy:  policy,
		Voting:  newVoting(),
		limiter: limiter,
	}

	vs.Store(policy.RewardEpochID, r)
}

// AddVote adds vote to a correct vote box in a correct round and returns a receipt.
// If a round does not exits an error is returned.
// If a VoteBox does not exist yet, a new VoteBox is created if the proposer is not limited.

func (s *Storage) AddVote(data *instruction.Data, signer common.Address, signature []byte) (*Receipt, error) {
	id := data.InstructionId

	hash, err := data.HashFixed()
	if err != nil {
		return nil, err
	}

	round, exists := s.Get(data.RewardEpochId)
	if !exists {
		return nil, fmt.Errorf("%w no round %d", status.HTTP[404], data.RewardEpochId)
	}

	err = s.meta.CheckConsistency(data, signer)
	if err != nil {
		return nil, fmt.Errorf("verifying message validity: %w", err)
	}

	// Do not allow creating two sets boxes at once. Release lock if set of boxes exists.
	round.Lock()
	boxes, exist := round.Voting.M[id]
	if !exist {
		boxes = newVotBoxes()
		defer round.Unlock()
		// we only save it at the end if no errors are returned
	} else {
		round.Unlock()
	}

	// Do not allow creating two boxes at once. Release lock if box exist.
	boxes.Lock()
	box, exist := boxes.M[hash]
	if !exist {
		box, err = s.startVoteBox(data, signer, round, id)
		defer boxes.Unlock()
		if err != nil {
			return nil, err
		}
		// we only save it at the end if no errors are returned
	} else {
		boxes.Unlock()
	}

	if box.deleted {
		return nil, fmt.Errorf("%w, voting already ended %d", status.HTTP[400], id)
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

	receipt, finished, err := box.addVote(signer, weight, signature, data.AdditionalVariableMessage, vg)
	if err != nil {
		return nil, fmt.Errorf("adding vote from %s to %v: %v", signer, id, err)
	}

	receipt.InstructionHash = hash

	// save (only needed if new are made otherwise ineffective)
	boxes.M[hash] = box
	round.Voting.M[id] = boxes

	if finished {
		round.limiter.Decrement(box.Proposer)
		s.OutThreshold <- box
	}

	return &receipt, nil
}
