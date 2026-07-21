package voting

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/storage"
	"github.com/flare-foundation/go-flare-common/pkg/voters"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction/voting/limiter"
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

	metrics           *metrics.Metrics
	collectVoters     bool
	currentEpoch      atomic.Uint32 // latest stored reward epoch, for the active-voter gauge
	maxConsensusEpoch atomic.Uint32 // highest reward epoch of any voting that reached consensus
}

// NewStorage creates a voting Storage backed by a cyclic buffer sized by config.HistorySize.
// ctx is the service-lifetime context that bounds per-box goroutines created during AddVote.
// m may be nil or disabled.
func NewStorage(ctx context.Context, config *config.Voting, meta meta.Meta, m *metrics.Metrics) *Storage {
	out := make(chan *types.Action, config.FinalizedBufferSize)

	return &Storage{
		Cyclic:        storage.New[uint32, *Round](config.HistorySize),
		config:        *config,
		meta:          meta,
		Out:           out,
		ctx:           ctx,
		metrics:       m,
		collectVoters: m.ActiveVotersEnabled(),
	}
}

// StoreNewRound creates and stores a round for the policy, overwriting the previous occupant
// at the same cyclic slot. A no-op if a round for this policy already exists.
func (s *Storage) StoreNewRound(policy *policy.SigningPolicy) {
	_, exists := s.Get(policy.RewardEpochID)
	if exists {
		return
	}

	r := createRound(policy, s.config.MaxPendingRequests, s.collectVoters)

	s.Store(policy.RewardEpochID, r)

	if policy.RewardEpochID > s.currentEpoch.Load() {
		s.currentEpoch.Store(policy.RewardEpochID)
	}
}

// recordConsensusEpoch raises the max-consensus-epoch watermark to epoch if it is larger.
func (s *Storage) recordConsensusEpoch(epoch uint32) {
	for {
		old := s.maxConsensusEpoch.Load()
		if epoch <= old {
			return
		}
		if s.maxConsensusEpoch.CompareAndSwap(old, epoch) {
			return
		}
	}
}

// MaxConsensusEpoch returns the highest reward epoch reached by consensus, or 0 if none yet.
func (s *Storage) MaxConsensusEpoch() uint32 {
	return s.maxConsensusEpoch.Load()
}

// activeParticipantEpochs returns the newest resident reward epoch and its immediate
// predecessor so a scrape-time gauge can report the max across the epoch-announcement
// overlap, when a freshly stored policy's round is still empty while the active epoch keeps
// voting into the previous round.
func (s *Storage) activeParticipantEpochs() []uint32 {
	cur := s.currentEpoch.Load()
	if cur == 0 {
		return []uint32{0}
	}
	return []uint32{cur, cur - 1}
}

// CurrentRoundProviderVoterCount returns the distinct data-provider-voter count for the
// latest reward epoch, or 0 if no round is stored. During an epoch-announcement overlap it
// reports the max of the newest resident round and its predecessor. Read at scrape time.
func (s *Storage) CurrentRoundProviderVoterCount() int {
	var count int
	for _, e := range s.activeParticipantEpochs() {
		if r, ok := s.Get(e); ok {
			count = max(count, r.ProviderVoterCount())
		}
	}
	return count
}

// CurrentRoundInitiatorCount returns the distinct initiator count for the latest reward
// epoch, or 0 if no round is stored. During an epoch-announcement overlap it reports the max
// of the newest resident round and its predecessor. Read at scrape time.
func (s *Storage) CurrentRoundInitiatorCount() int {
	var count int
	for _, e := range s.activeParticipantEpochs() {
		if r, ok := s.Get(e); ok {
			count = max(count, r.ProposerCount())
		}
	}
	return count
}

// CurrentRoundTopPending returns up to n voters with the most unfinalised proposals in the
// latest reward epoch, or nil if no round is stored. During an epoch-announcement overlap it
// merges the newest resident round and its predecessor, taking the max pending per provider
// (not the sum, so a provider present in both rounds is not double-counted). Read at scrape time.
func (s *Storage) CurrentRoundTopPending(n int) []limiter.VoterPending {
	merged := map[common.Address]uint{}
	for _, e := range s.activeParticipantEpochs() {
		r, ok := s.Get(e)
		if !ok {
			continue
		}
		for _, vp := range r.TopPendingProposals(-1) { // -1 returns all pending voters
			merged[vp.Address] = max(merged[vp.Address], vp.Pending)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out := make([]limiter.VoterPending, 0, len(merged))
	for addr, p := range merged {
		out = append(out, limiter.VoterPending{Address: addr, Pending: p})
	}
	return limiter.SortTopPending(out, n)
}

// OldestStoredEpoch returns the oldest reward epoch with a round still resident in the cyclic
// buffer, with ok=false when none is stored. The newest resident epoch is the active one,
// already exposed as policy_active_reward_epoch. Read at scrape time.
func (s *Storage) OldestStoredEpoch() (uint32, bool) {
	top := s.currentEpoch.Load()
	var oldest uint32
	var ok bool
	for i := range uint32(s.Size()) {
		if i > top {
			break
		}
		e := top - i
		if _, present := s.Get(e); present {
			oldest = e
			ok = true
		}
		if e == 0 {
			break
		}
	}
	return oldest, ok
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
	var reached bool
	var startedBox bool
	var isProvider bool
	var buildErr bool
	var thresholdDuration time.Duration

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
		isProvider = vg&provider != 0

		r, finalized, err := box.addVote(signer, weight, signature, data.AdditionalVariableMessage, vg)
		if err != nil {
			return fmt.Errorf("adding vote from %s to %v: %w", signer, id, err)
		}
		receipt = r

		if !existsB {
			boxes.M[hash] = box
			go box.scheduleEnd(s.ctx, s.Out, boxes, s.metrics)
			startedBox = true
		}

		if !existsBs {
			round.Voting.M[id] = boxes
		}

		if finalized {
			reached = true
			thresholdDuration = time.Since(box.StartTime)
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
					logger.Errorf("failed creating threshold action for %v, %v: %v", id, hash, err)
					buildErr = true
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

	// Record metrics outside the locked critical section above.
	if startedBox {
		s.metrics.VotingStarted()
	}
	if s.collectVoters {
		if isProvider {
			round.markProviderVoter(signer)
		}
		if startedBox {
			round.markProposer(signer)
		}
	}
	if reached {
		s.metrics.VotingThresholdReached(thresholdDuration)
		s.recordConsensusEpoch(data.RewardEpochID)
	}
	if buildErr {
		s.metrics.FinalizedActionLost("build_error")
	}

	if actionToSend != nil {
		select {
		case s.Out <- actionToSend:
		case <-s.ctx.Done():
			s.metrics.FinalizedActionLost("send_cancelled")
			return nil, fmt.Errorf("forwarding finalized action: %w", s.ctx.Err())
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
