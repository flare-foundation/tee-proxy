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

	sync.Mutex
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
func createRound(policy *policy.SigningPolicy, maxPendingRequests uint) *Round {
	limiter := limiter.New(policy.Voters.Voters(), maxPendingRequests)

	return &Round{
		policy:  policy,
		Voting:  newVoting(),
		limiter: limiter,
	}
}
