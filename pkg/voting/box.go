package voting

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"

	"github.com/flare-foundation/tee-node/pkg/types"
)

type proposal struct {
	Instruction *instruction.DataFixed

	Threshold uint16

	Cosigners         map[common.Address]bool // list of cosigners extracted in the first vote from key metadata or from attestation request (FTDC).
	CosignerThreshold uint16                  // cosigner threshold, extracted on the vote, like cosigners.

	// Todo: VoteHash (dynamically calculated on the fly or stored and modified at each update)

	// additional data
	RequestPolicy *policy.SigningPolicy
	Result        []byte

	sync.Mutex
}

func newProposal(data *instruction.DataFixed, threshold uint16, cosigners map[common.Address]bool, cosignerThreshold uint64) *proposal {
	return &proposal{
		Instruction: data,

		Threshold: threshold,

		Cosigners:         cosigners,
		CosignerThreshold: uint16(cosignerThreshold),
	}
}

type vote struct {
	Sequence                  uint64
	Time                      time.Time
	Signature                 []byte
	AdditionalVariableMessage []byte
}

type voteBox struct {
	Proposer common.Address

	proposal *proposal
	votes    map[common.Address]*vote

	VoteHash common.Hash

	StartTime time.Time
	EndTime   time.Time

	weight         uint16
	cosignerWeight uint16

	finalized bool
	deleted   bool

	sync.RWMutex
}

func newVoteBox(data *instruction.DataFixed, proposer common.Address, threshold uint16, cosigners map[common.Address]bool, cosignerThreshold uint64) (*voteBox, error) {
	proposal := newProposal(data, threshold, cosigners, cosignerThreshold)

	now := time.Now()
	end := now.Add(proposalExpiration)

	hash, err := data.InitialVoteHash()
	if err != nil {
		return nil, fmt.Errorf("computing initial hash %v", err)
	}

	vb := &voteBox{
		Proposer:       proposer,
		proposal:       proposal,
		votes:          map[common.Address]*vote{},
		VoteHash:       hash,
		StartTime:      now,
		EndTime:        end,
		weight:         0,
		cosignerWeight: 0,
		finalized:      false,
		deleted:        false,
	}

	return vb, nil
}

// delete clears voteBox and sets it's deleted status to true.
func (vb *voteBox) delete() {
	vb.proposal = nil
	vb.votes = nil

	vb.deleted = true
}

func (vb *voteBox) action(tag types.SubmissionTag) (*types.Action, error) {
	if vb.deleted {
		return nil, errors.New("already deleted")
	}

	if !vb.finalized {
		return nil, errors.New("not yet finalized")
	}

	m, err := json.Marshal(vb.proposal.Instruction)
	if err != nil {
		return nil, fmt.Errorf("marshaling action data, %v", err)
	}

	ad := types.ActionData{
		ID:            vb.proposal.Instruction.InstructionID,
		Type:          types.Instruction,
		SubmissionTag: tag,
		Message:       m,
	}

	s, avm, ts := vb.signersData()

	a := &types.Action{
		Data:                       ad,
		Signatures:                 s,
		AdditionalVariableMessages: avm,
		Timestamps:                 ts,
		AdditionalActionData:       []byte{},
	}

	return a, nil
}

func (vb *voteBox) addVote(signer common.Address, weight uint16, signature []byte, additionalVariableMessage []byte, voterGroup voterGroup) (Receipt, bool, error) {
	var receipt Receipt

	if voterGroup == invalidVoter {
		return receipt, false, errors.New("invalid voter")
	}

	now := time.Now()

	if vb.EndTime.Before(now) {
		return receipt, false, fmt.Errorf("voting already ended")
	}
	if _, exists := vb.votes[signer]; exists {
		return receipt, false, fmt.Errorf("signature from %s already added", signer)
	}

	vote := &vote{
		Sequence:                  uint64(len(vb.votes)),
		Time:                      now,
		Signature:                 signature,
		AdditionalVariableMessage: additionalVariableMessage,
	}

	hash, err := instruction.NextVoteHash(vb.VoteHash, signer, signature, additionalVariableMessage, uint64(now.Unix()))
	if err != nil {
		return receipt, false, fmt.Errorf("calculating next hash: %v", err)
	}
	vb.VoteHash = hash
	vb.votes[signer] = vote

	vb.weight += weight

	if voterGroup.isCosigner() {
		vb.cosignerWeight++
	}

	receipt = Receipt{
		InstructionHash:               common.Hash{}, // to be added in the calling function
		Sequence:                      vote.Sequence,
		AdditionalVariableMessageHash: crypto.Keccak256Hash(additionalVariableMessage),
		Timestamp:                     uint64(now.Unix()),
		VoteHash:                      hash,
	}

	if !vb.finalized && vb.weight >= vb.proposal.Threshold && vb.cosignerWeight >= vb.proposal.CosignerThreshold {
		vb.finalized = true
		return receipt, true, nil
	}

	return receipt, false, nil
}

func (vb *voteBox) signersData() (signatures []hexutil.Bytes, additionalVariableMessages []hexutil.Bytes, timestamps []uint64) {
	signatures = make([]hexutil.Bytes, len(vb.votes))
	additionalVariableMessages = make([]hexutil.Bytes, len(vb.votes))
	timestamps = make([]uint64, len(vb.votes))

	for _, vote := range vb.votes {
		j := vote.Sequence

		signatures[j] = vote.Signature
		additionalVariableMessages[j] = vote.AdditionalVariableMessage
		timestamps[j] = uint64(vote.Time.Unix())
	}

	return signatures, additionalVariableMessages, timestamps
}
