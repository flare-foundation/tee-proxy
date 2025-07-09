package voting

import (
	"fmt"
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

const proposalExpiration = 120 * time.Second // MORE??

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
	*storage.Cyclic[uint64, *Round]

	// Metadata for the voting process.
	meta meta.Meta

	// Channel for boxes that reach threshold.
	OutThreshold chan *voteBox
	// Channel for boxes that reach the end of voting.
	OutEnd chan *voteBox
}

// NewStorage returns new Storage with
func NewStorage(size int, meta meta.Meta, outThreshold, outEnd chan *voteBox) *Storage {
	return &Storage{storage.New[uint64, *Round](size), meta, outThreshold, outEnd}
}

type Round struct {
	policy      *policy.SigningPolicy
	VotingBoxes map[common.Hash](map[common.Hash]*voteBox) // instructionID -> instructionHash -> VoteBox

	limiter *limiter.Limiter
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
		policy:      policy,
		VotingBoxes: map[common.Hash]map[common.Hash]*voteBox{},
		limiter:     limiter,
	}

	vs.Store(uint64(policy.RewardEpochID), r)
}

// AddVote adds vote to a correct vote box in a correct round and returns a receipt.
// If a round does not exits an error is returned.
// If a voteBox does not exist yet, a new voteBox is created if the proposer is not limited.

func (s *Storage) AddVote(data *instruction.Data, signer common.Address, signature []byte) (*Receipt, error) {
	id := data.InstructionID

	hash, err := data.HashFixed()
	if err != nil {
		return nil, err
	}

	if !data.RewardEpochID.IsUint64() {
		return nil, fmt.Errorf("%w, reward epoch overflow", status.HTTP[400])
	}
	reID := data.RewardEpochID.Uint64()

	round, exists := s.Get(reID)
	if !exists {
		return nil, fmt.Errorf("%w no round %d", status.HTTP[404], reID)
	}

	boxes, exist := round.VotingBoxes[id]
	if !exist {
		boxes = make(map[common.Hash]*voteBox)
		// we only save it at the end if no errors are returned
	}

	box, exist := boxes[hash]
	if !exist {
		t, err := s.meta.ThresholdBIPS(&data.DataFixed)
		if err != nil {
			return nil, fmt.Errorf("cannot get threshold for %v", id)
		}

		var threshold uint16
		switch {
		case t == -1:
			threshold = round.policy.Threshold
		case t < -1 || t > maxBIPS:
			return nil, fmt.Errorf("invalid threshold %d for %v", t, id)
		default:
			threshold = computeThreshold(round.policy.Voters.TotalWeight, t)
		}

		cosigners, cosignerThreshold, err := s.meta.Cosigners(&data.DataFixed)
		if err != nil {
			return nil, fmt.Errorf("cannot get cosigners for %v: %w", id, err)
		}

		if cosigners[signer] {
			round.limiter.Add(signer)
		}

		err = round.limiter.Increment(signer)
		if err != nil {
			return nil, err
		}

		box, err = newVoteBox(&data.DataFixed, signer, threshold, cosigners, cosignerThreshold)
		// we only save it at the end if no errors are returned
		if err != nil {
			return nil, fmt.Errorf("cannot create new vote box %w", err)
		}

		go func() {
			time.Sleep(time.Until(box.EndTime))

			s.OutEnd <- box
		}()
	}

	var vg voterGroup = 0
	weight := uint16(0)

	vd, exists := round.policy.Voters.VoterDataMap[signer]
	if exists {
		vg |= 0b01
		weight = vd.Weight
	}

	if box.proposal.Cosigners[signer] {
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
	boxes[hash] = box
	round.VotingBoxes[id] = boxes

	if finished {
		round.limiter.Decrement(box.Proposer)
		s.OutThreshold <- box
	}

	return &receipt, nil
}

// computeThreshold matches the computation of the threshold for signing policy.
// It is assumed that 0 <= bips <= 10000.
func computeThreshold(total uint16, bips int) uint16 {
	t64 := uint64(total)
	b64 := uint64(bips)
	t := t64 * b64 / maxBIPS

	if (t64*b64)%maxBIPS != 0 {
		t++
	}

	return uint16(t) //nolint:gosec
}
