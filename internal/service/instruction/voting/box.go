package voting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/flare-foundation/tee-node/pkg/types"
)

var (
	errVotingBeforeEvent      = fmt.Errorf("%w: voting started before the event", status.HTTP[403])
	errActionAlreadyDeleted   = errors.New("already deleted")
	errActionNotFinalized     = errors.New("not finalized")
	errInvalidVoter           = fmt.Errorf("%w: invalid voter", status.HTTP[403])
	errVotingEnded            = fmt.Errorf("%w: voting already ended", status.HTTP[403])
	errSignatureAlreadyStored = fmt.Errorf("%w: signature already stored", status.HTTP[403])
)

// eventFutureSlack is the allowed slippage between the local clock and the
// event timestamp: how far into the future (relative to local time) an
// instruction's event can claim to be while still accepted.
const eventFutureSlack = 15 * time.Second

type proposal struct {
	instruction *instruction.DataFixed

	threshold uint16

	cosigners         map[common.Address]bool
	cosignerThreshold uint16
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

// startVoteBox
func startVoteBox(data *instruction.Data, signer common.Address, round *Round, meta meta.Meta, expirationTime time.Duration) (*voteBox, error) {
	eventTime := time.Unix(int64(data.Timestamp), 0)

	allowedTime := eventTime.Add(-eventFutureSlack)

	if time.Now().Before(allowedTime) {
		return nil, errVotingBeforeEvent
	}

	t, err := meta.ThresholdBIPS(&data.DataFixed)
	if err != nil {
		return nil, fmt.Errorf("reading threshold: %w", err)
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

	cosigners, cosignerThreshold, err := meta.Cosigners(&data.DataFixed)
	if err != nil {
		return nil, fmt.Errorf("reading cosigners: %w", err)
	}

	err = round.limiter.Increment(signer)
	if err != nil {
		return nil, err
	}

	box, err := newVoteBox(&data.DataFixed, signer, threshold, cosigners, cosignerThreshold)
	if err != nil {
		return nil, fmt.Errorf("creating new vote box: %w", err)
	}

	box.StartTime = time.Now()
	box.EndTime = box.StartTime.Add(expirationTime)

	return box, nil
}

// newVoteBox assembles a new VoteBox.
//
// StartTime and EndTime should be set by the calling function.
func newVoteBox(data *instruction.DataFixed, proposer common.Address, threshold uint16, cosigners map[common.Address]bool, cosignerThreshold uint64) (*voteBox, error) {
	proposal := newProposal(data, threshold, cosigners, cosignerThreshold)

	hash, err := data.InitialVoteHash()
	if err != nil {
		return nil, fmt.Errorf("computing initial hash: %w", err)
	}
	iHash, err := data.HashFixed()
	if err != nil {
		return nil, err
	}

	vb := &voteBox{
		iID:            data.InstructionID,
		iHash:          iHash,
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
// Mutex must be handled by the calling function.
func (vb *voteBox) Action(tag types.SubmissionTag) (*types.Action, error) {
	if vb.deleted {
		return nil, errActionAlreadyDeleted
	}

	if !vb.Finalized {
		return nil, errActionNotFinalized
	}

	m, err := json.Marshal(vb.proposal.instruction)
	if err != nil {
		return nil, fmt.Errorf("marshaling action data: %w", err)
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

// Status returns the current status of the box.
func (vb *voteBox) Status(hash common.Hash) voting.Status {
	vb.RLock()
	defer vb.RUnlock()

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

// delete clears VoteBox and sets it's deleted status to true.
// Mutex must be handled by the calling function.
func (vb *voteBox) delete() {
	vb.proposal.cosigners = nil
	vb.proposal.instruction = nil

	vb.votes = nil

	vb.deleted = true
}

// addVote adds vote to a VoteBox and returns a Receipt and a boolean indicator of finalization,
// that is true only the first time the conditions for finalization are fulfilled.
//
// Mutex has to be managed by the calling function.
func (vb *voteBox) addVote(signer common.Address, weight uint16, signature []byte, additionalVariableMessage []byte, voterGroup voterGroup) (voting.Receipt, bool, error) {
	if voterGroup == invalidVoter {
		return voting.Receipt{}, false, errInvalidVoter
	}

	now := time.Now()
	if vb.EndTime.Before(now) {
		return voting.Receipt{}, false, errVotingEnded
	}
	if _, exists := vb.votes[signer]; exists {
		return voting.Receipt{}, false, errSignatureAlreadyStored
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
		return voting.Receipt{}, false, fmt.Errorf("computing next vote hash: %w", err)
	}
	vb.votes[signer] = vote

	vb.weight += weight

	if voterGroup.isCosigner() {
		vb.cosignerWeight++
	}

	receipt := voting.Receipt{
		InstructionHash:               vb.iHash,
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

func (vb *voteBox) scheduleEnd(ctx context.Context, out chan *types.Action, boxes *voteBoxes) {
	// Cancellable wait so shutdown doesn't leak per-box goroutines.
	if d := time.Until(vb.EndTime); d > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}

	// Build actions under the locks, send after — a full `out` must not block the locks.
	actions := func() []*types.Action {
		// Acquire boxes.RLock before vb.Lock so the order matches AddVote
		// (boxes → box) and avoids a lock-order inversion deadlock.
		boxes.RLock()
		defer boxes.RUnlock()

		vb.Lock()
		defer vb.Unlock()

		defer vb.delete()

		opCommand := vb.proposal.instruction.OPCommand
		opType := vb.proposal.instruction.OPType

		if !vb.Finalized {
			logger.Debugf("closing non finalized box %v, %v", vb.iID, vb.iHash)
			return nil
		}

		var as []*types.Action

		// send threshold action for KeyDataProviderRestore at the end of voting
		if opType == op.Wallet.Hash() && opCommand == op.KeyDataProviderRestore.Hash() {
			a, err := vb.Action(types.Threshold)
			if err != nil {
				logger.Errorf("failed creating threshold action for %v, %v: %v", vb.iID, vb.iHash, err)
			} else {
				as = append(as, a)
			}
		} else if vb.iHash != boxes.FinalizedHash {
			logger.Debugf("closing finalized box %v, %v that was finalized after %v", vb.iID, vb.iHash, boxes.FinalizedHash)
			return as
		}

		a, err := vb.Action(types.End)
		if err != nil {
			logger.Errorf("failed creating end action for %v, %v: %v", vb.iID, vb.iHash, err)
		} else {
			as = append(as, a)
		}

		return as
	}()

	for _, a := range actions {
		out <- a
	}
}

// signersData returns slices of signatures, additionalVariableMessages, and timestamps.
// signature, additionalVariableMessages, and timestamps in slot j come from the same vote.
// Slices are sorted according to the arrival of votes.
//
// Mutex has to be handled by the calling function.
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
