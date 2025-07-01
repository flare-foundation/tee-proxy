package voting

import (
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/storage"
	"github.com/flare-foundation/tee-proxy/pkg/limiter"
	"github.com/flare-foundation/tee-proxy/pkg/meta"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
)

const proposalExpiration = 120 * time.Second // MORE??

const maxPendingRequests = 100 // MORE??

type voterGroup uint8

const (
	invalidVoter voterGroup = 0
	provider     voterGroup = 0b01
	cosigner     voterGroup = 0b10
	both         voterGroup = 0b11
)

func (vg voterGroup) isCosigner() bool {
	return vg&cosigner != 0
}

type Storage struct {
	*storage.Cyclic[uint64, *Round]

	meta meta.Meta

	OutThreshold chan *voteBox // maybe a copy
	OutEnd       chan *voteBox // todo: decide on type

}

type VoterType uint8

func NewStorage(size int, meta meta.Meta, outThreshold, outEnd chan *voteBox) *Storage {
	return &Storage{storage.New[uint64, *Round](size), meta, outThreshold, outEnd}
}

type Round struct {
	policy      *policy.SigningPolicy
	VotingBoxes map[common.Hash](map[common.Hash]*voteBox) // instructionID -> instructionHash -> VoteBox

	limiter *limiter.Limiter
}

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
		// we only save it if no errors are returned
	}

	box, exist := boxes[hash]
	if !exist {
		t, err := s.meta.Threshold(&data.DataFixed)
		if err != nil {
			return nil, fmt.Errorf("cannot get threshold for %v", id)
		}

		var threshold uint16
		if t == -1 {
			threshold = round.policy.Threshold
		} else if t < -1 || t > math.MaxUint16 {
			return nil, fmt.Errorf("invalid threshold %d for %v", t, id)
		} else {
			totalWeight := big.NewInt(int64(round.policy.Voters.TotalWeight))
			tBIPS := big.NewInt(int64(t))

			totalWeight.Mul(totalWeight, tBIPS)
			tBig := totalWeight.Div(totalWeight, big.NewInt(10000))

			tUin64 := tBig.Uint64()

			//todo make safe conversion
			threshold = uint16(tUin64)
		}

		cosigners, cosignerThreshold, err := s.meta.Cosigners(&data.DataFixed)
		if err != nil {
			return nil, fmt.Errorf("cannot get cosigners for %v: %w", id, err)
		}

		if cosigners[signer] {
			round.limiter.Add(signer)
		}

		if err := round.limiter.Increment(signer); err != nil {
			return nil, err
		}

		box, err = newVoteBox(&data.DataFixed, signer, threshold, cosigners, cosignerThreshold)
		// we only save it if no errors are returned
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
