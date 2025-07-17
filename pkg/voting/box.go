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
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/flare-foundation/tee-node/pkg/types"
)

type proposal struct {
	Instruction *instruction.DataFixed

	Threshold uint16

	Cosigners         map[common.Address]bool // list of cosigners extracted in the first vote from key metadata or from attestation request (FTDC).
	CosignerThreshold uint16                  // cosigner threshold, extracted on the vote, like cosigners.

	// additional data
	RequestPolicy *policy.SigningPolicy
	Result        []byte

	sync.Mutex
}

// newProposal assembles a new proposal.
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

// voteBox holds one voting process.
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

// newVoteBox assembles new VoteBox.
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

// delete clears VoteBox and sets it's deleted status to true.
func (vb *voteBox) Delete() {
	vb.proposal = nil
	vb.votes = nil

	vb.deleted = true
}

// Action creates Action with provided tag from a finalized VoteBox.
func (vb *voteBox) Action(tag types.SubmissionTag) (*types.Action, error) {
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

// addVote adds vote to a VoteBox and returns a Receipt and a boolean indicator of finalization,
// that is true only the first time the conditions for finalization are fulfilled.
//
// The returned receipt has zero valued Instruction Hash that has to be filled in the calling function.
func (vb *voteBox) addVote(signer common.Address, weight uint16, signature []byte, additionalVariableMessage []byte, voterGroup voterGroup) (Receipt, bool, error) {
	var receipt Receipt

	if voterGroup == invalidVoter {
		return receipt, false, fmt.Errorf("%w: invalid voter", status.HTTP[403])
	}

	now := time.Now()

	if vb.EndTime.Before(now) {
		return receipt, false, fmt.Errorf("%w: voting already ended", status.HTTP[403])
	}
	if _, exists := vb.votes[signer]; exists {
		return receipt, false, fmt.Errorf("%w: signature already stored", status.HTTP[403])
	}

	vote := &vote{
		Sequence:                  uint64(len(vb.votes)),
		Time:                      now,
		Signature:                 signature,
		AdditionalVariableMessage: additionalVariableMessage,
	}

	vb.VoteHash = instruction.NextVoteHash(vb.VoteHash, signer, signature, additionalVariableMessage, uint64(now.Unix()))
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
		VoteHash:                      vb.VoteHash,
	}

	if !vb.finalized && vb.weight > vb.proposal.Threshold && vb.cosignerWeight >= vb.proposal.CosignerThreshold {
		vb.finalized = true
		return receipt, true, nil
	}

	return receipt, false, nil
}

func (s *Storage) startVoteBox(data *instruction.Data, signer common.Address, round *Round, id common.Hash) (*voteBox, error) {
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

	box, err := newVoteBox(&data.DataFixed, signer, threshold, cosigners, cosignerThreshold)
	// we only save it at the end if no errors are returned
	if err != nil {
		return nil, fmt.Errorf("cannot create new vote box %w", err)
	}

	go func() {
		time.Sleep(time.Until(box.EndTime))

		s.OutEnd <- box
	}()

	return box, nil
}

// signersData returns slices of signatures, additionalVariableMessages, and timestamps.
// signature, additionalVariableMessages, and timestamps in slot j come from the same vote.
// Slices are sorted according to the arrival of votes.
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
