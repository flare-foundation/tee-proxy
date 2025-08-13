package instruction

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/flare-foundation/tee-node/pkg/types"
)

type proposal struct {
	instruction *instruction.DataFixed

	threshold uint16

	cosigners         map[common.Address]bool // list of cosigners extracted in the first vote from key metadata or from attestation request (FTDC).
	cosignerThreshold uint16                  // cosigner threshold, extracted on the vote, like cosigners.

	sync.Mutex
}

// newProposal assembles a new proposal.
func newProposal(data *instruction.DataFixed, threshold uint16, cosigners map[common.Address]bool, cosignerThreshold uint64) *proposal {
	return &proposal{
		instruction: data,

		threshold: threshold,

		cosigners:         cosigners,
		cosignerThreshold: uint16(cosignerThreshold),
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
	iID   common.Hash
	iHash common.Hash

	Proposer common.Address

	proposal *proposal
	votes    map[common.Address]*vote

	VoteHash common.Hash

	StartTime time.Time
	EndTime   time.Time

	weight         uint16
	cosignerWeight uint16

	Finalized bool
	deleted   bool

	sync.RWMutex
}

// newVoteBox assembles new VoteBox.
//
// StartTime and EndTime should be set by the calling function.
func newVoteBox(data *instruction.DataFixed, proposer common.Address, threshold uint16, cosigners map[common.Address]bool, cosignerThreshold uint64) (*voteBox, error) {
	proposal := newProposal(data, threshold, cosigners, cosignerThreshold)

	hash, err := data.InitialVoteHash()
	if err != nil {
		return nil, fmt.Errorf("computing initial hash %v", err)
	}

	vb := &voteBox{
		Proposer:       proposer,
		proposal:       proposal,
		votes:          map[common.Address]*vote{},
		VoteHash:       hash,
		weight:         0,
		cosignerWeight: 0,
		Finalized:      false,
		deleted:        false,
	}

	return vb, nil
}

// Action creates Action with provided tag from a finalized VoteBox.
func (vb *voteBox) Action(tag types.SubmissionTag) (*types.Action, error) {
	if vb.deleted {
		return nil, errors.New("already deleted")
	}

	if !vb.Finalized {
		return nil, errors.New("not yet finalized")
	}

	m, err := json.Marshal(vb.proposal.instruction)
	if err != nil {
		return nil, fmt.Errorf("marshaling action data, %v", err)
	}

	ad := types.ActionData{
		ID:            vb.proposal.instruction.InstructionID,
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

// delete clears VoteBox and sets it's deleted status to true.
func (vb *voteBox) Delete() {
	vb.proposal.cosigners = nil
	vb.proposal.instruction = nil

	vb.votes = nil

	vb.deleted = true
}

// delete clears VoteBox and sets it's deleted status to true.
func (vb *voteBox) Status(hash common.Hash) voting.Status {
	var threshold, cosignersThreshold uint16 = 0, 0
	if vb.proposal != nil {
		threshold = vb.proposal.threshold
		cosignersThreshold = vb.proposal.cosignerThreshold
	}

	return voting.Status{
		InstructionHash:    vb.iHash,
		Finalized:          vb.Finalized,
		Deleted:            vb.deleted,
		Start:              uint64(vb.StartTime.Unix()),
		End:                uint64(vb.EndTime.Unix()),
		Weight:             vb.weight,
		Threshold:          threshold,
		Cosigners:          vb.cosignerWeight,
		CosignersThreshold: cosignersThreshold,
	}
}

// addVote adds vote to a VoteBox and returns a Receipt and a boolean indicator of finalization,
// that is true only the first time the conditions for finalization are fulfilled.
//
// The returned receipt has zero valued Instruction Hash that has to be filled in the calling function.
func (vb *voteBox) addVote(signer common.Address, weight uint16, signature []byte, additionalVariableMessage []byte, voterGroup voterGroup) (voting.Receipt, bool, error) {
	var receipt voting.Receipt

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

	seq := uint64(len(vb.votes))
	vote := &vote{
		Sequence:                  seq,
		Time:                      now,
		Signature:                 signature,
		AdditionalVariableMessage: additionalVariableMessage,
	}

	var err error
	vb.VoteHash, err = instruction.NextVoteHash(vb.VoteHash, seq, signature, additionalVariableMessage, uint64(now.Unix()))
	if err != nil {
		return receipt, false, fmt.Errorf("computing next vote hash: %w", err)
	}
	vb.votes[signer] = vote

	vb.weight += weight

	if voterGroup.isCosigner() {
		vb.cosignerWeight++
	}

	receipt = voting.Receipt{
		InstructionHash:               common.Hash{}, // to be added in the calling function
		Sequence:                      vote.Sequence,
		Signature:                     signature,
		AdditionalVariableMessageHash: crypto.Keccak256Hash(additionalVariableMessage),
		Timestamp:                     uint64(now.Unix()),
		VoteHash:                      vb.VoteHash,
	}

	if !vb.Finalized && vb.weight > vb.proposal.threshold && vb.cosignerWeight >= vb.proposal.cosignerThreshold {
		vb.Finalized = true
		return receipt, true, nil
	}

	return receipt, false, nil
}

// startVoteBox
func (s *Storage) startVoteBox(data *instruction.Data, signer common.Address, round *Round) (*voteBox, error) {
	eventTime := time.Unix(int64(data.Timestamp), 0)

	allowedTime := eventTime.Add(-15 * time.Second) // confirm this number

	if time.Now().Before(allowedTime) {
		return nil, fmt.Errorf("%w: voting started before the event", status.HTTP[403])
	}

	t, err := s.meta.ThresholdBIPS(&data.DataFixed)
	if err != nil {
		return nil, fmt.Errorf("cannot get threshold")
	}

	var threshold uint16
	switch {
	case t == -1:
		threshold = round.policy.Threshold
	case t < -1 || t > maxBIPS:
		return nil, fmt.Errorf("invalid threshold %d", t)
	default:
		threshold = computeThreshold(round.policy.Voters.TotalWeight, t)
	}

	cosigners, cosignerThreshold, err := s.meta.Cosigners(&data.DataFixed)
	if err != nil {
		return nil, fmt.Errorf("cannot get cosigners for %w", err)
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

	box.StartTime = time.Now()
	box.EndTime = box.StartTime.Add(s.config.ProposalExpiration)

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
