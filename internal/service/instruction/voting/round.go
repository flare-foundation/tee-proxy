package voting

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction/voting/limiter"
)

// Round holds all voting activity that belongs to one signing policy (reward epoch).
type Round struct {
	policy *policy.SigningPolicy
	Voting votingSync // instructionID -> instructionHash -> VoteBox

	limiter *limiter.Limiter

	// votersMu guards the participant sets and the weight watermark below. They stay nil/zero
	// (and are never populated) unless active-voter metrics are collected.
	votersMu sync.Mutex
	// providerVoters tracks distinct data-provider addresses (in the policy's voter set) that
	// cast an accepted vote this epoch.
	providerVoters map[common.Address]struct{}
	// proposers tracks distinct addresses that opened a voting (initiators) this epoch.
	proposers map[common.Address]struct{}
	// maxVotingWeight is the highest provider weight any single voting accumulated this epoch.
	maxVotingWeight uint16
}
type voteBoxes struct {
	M map[common.Hash]*voteBox

	FinalizedHash common.Hash

	sync.RWMutex
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

func newVoteBoxes() *voteBoxes {
	return &voteBoxes{
		M: map[common.Hash]*voteBox{},
	}
}

// createRound creates a new round with a set limiter.
// When collectVoters is set, the round tracks distinct participants for the active-voter gauges.
func createRound(policy *policy.SigningPolicy, maxPendingRequests uint, collectVoters bool) *Round {
	limiter := limiter.New(policy.Voters.Voters(), maxPendingRequests)

	var providerVoters, proposers map[common.Address]struct{}
	if collectVoters {
		providerVoters = make(map[common.Address]struct{})
		proposers = make(map[common.Address]struct{})
	}

	return &Round{
		policy:         policy,
		Voting:         newVoting(),
		limiter:        limiter,
		providerVoters: providerVoters,
		proposers:      proposers,
	}
}

// markProviderVoter records that data-provider signer cast an accepted vote this epoch.
// No-op when the round is not tracking voters.
func (r *Round) markProviderVoter(signer common.Address) {
	if r.providerVoters == nil {
		return
	}
	r.votersMu.Lock()
	r.providerVoters[signer] = struct{}{}
	r.votersMu.Unlock()
}

// ProviderVoterCount returns the number of distinct data-provider voters seen this epoch.
func (r *Round) ProviderVoterCount() int {
	r.votersMu.Lock()
	defer r.votersMu.Unlock()
	return len(r.providerVoters)
}

// VotedWeightBips returns the combined signing-policy weight of this epoch's data-provider
// voters, in BIPS of the policy's total voter weight.
func (r *Round) VotedWeightBips() float64 {
	total := r.policy.Voters.TotalWeight
	if total == 0 {
		return 0 // guard: float division would emit an Inf/NaN sample
	}

	r.votersMu.Lock()
	defer r.votersMu.Unlock()

	var sum uint32 // never accumulate in uint16: sum*maxBIPS reaches 655_350_000
	for addr := range r.providerVoters {
		vd, ok := r.policy.Voters.VoterDataMap[addr]
		if !ok {
			continue // membership holds today; skipping keeps the scrape path panic-free
		}
		sum += uint32(vd.Weight)
	}

	return float64(sum) * maxBIPS / float64(total)
}

// recordVotingWeight raises the epoch's voting-weight watermark to w if it is larger.
// No-op when the round is not tracking voters.
func (r *Round) recordVotingWeight(w uint16) {
	if r.providerVoters == nil {
		return
	}
	r.votersMu.Lock()
	r.maxVotingWeight = max(r.maxVotingWeight, w)
	r.votersMu.Unlock()
}

// MaxVotingWeightBips returns the highest provider weight accumulated by any single voting
// this epoch, in BIPS of the policy's total voter weight.
func (r *Round) MaxVotingWeightBips() float64 {
	total := r.policy.Voters.TotalWeight
	if total == 0 {
		return 0
	}

	r.votersMu.Lock()
	defer r.votersMu.Unlock()

	return float64(r.maxVotingWeight) * maxBIPS / float64(total)
}

// ThresholdBips returns the policy's finalization threshold in BIPS of its total voter weight.
// Policy-derived, so it is independent of participation.
func (r *Round) ThresholdBips() float64 {
	total := r.policy.Voters.TotalWeight
	if total == 0 {
		return 0
	}

	return float64(r.policy.Threshold) * maxBIPS / float64(total)
}

// markProposer records that signer opened a voting (was an initiator) this epoch.
// No-op when the round is not tracking voters.
func (r *Round) markProposer(signer common.Address) {
	if r.proposers == nil {
		return
	}
	r.votersMu.Lock()
	r.proposers[signer] = struct{}{}
	r.votersMu.Unlock()
}

// ProposerCount returns the number of distinct initiators (proposers) seen this epoch.
func (r *Round) ProposerCount() int {
	r.votersMu.Lock()
	defer r.votersMu.Unlock()
	return len(r.proposers)
}

// TopPendingProposals returns up to n voters with the most unfinalised proposals this epoch,
// highest first, excluding voters with none.
func (r *Round) TopPendingProposals(n int) []limiter.VoterPending {
	return r.limiter.TopPending(n)
}
