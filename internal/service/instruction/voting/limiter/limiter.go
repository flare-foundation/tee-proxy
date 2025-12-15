package limiter

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

var ErrCannotInitialize = fmt.Errorf("%w: cannot initialize voting", status.HTTP[403])
var ErrLimitReached = fmt.Errorf("%w: propose limit reached", status.HTTP[429])

type Limiter struct {
	counter map[common.Address]*State

	maxPendingRequests uint

	sync.RWMutex
}

// State tracks the counts for a voter.
type State struct {
	// Address common.Address
	pending uint
	// Note: The following two fields can be used to calculate the ratio of completed to proposed requests.
	// Note: This could be used to detect if a validator is malicious or just working incorrectly.
	TotalProposed  int
	TotalCompleted int
}

// New creates a new Limiter that holds counters for <size> rounds and allows at mosts maxPendingRequests per voter.
func New(voters []common.Address, maxPendingRequests uint) *Limiter {
	c := make(map[common.Address]*State)

	for _, voter := range voters {
		c[voter] = &State{
			pending:        0,
			TotalProposed:  0,
			TotalCompleted: 0,
		}
	}

	return &Limiter{
		counter:            c,
		maxPendingRequests: maxPendingRequests,
	}
}

// Add adds zero state for an address if it is not already present.
func (l *Limiter) Add(address common.Address) {
	l.Lock()
	defer l.Unlock()

	_, exists := l.counter[address]
	if !exists {
		l.counter[address] = &State{
			pending:        0,
			TotalProposed:  0,
			TotalCompleted: 0,
		}
	}
}

// Increment increments counter for address in round and returns error if address is not eligible start the vote.
func (l *Limiter) Increment(address common.Address) error {
	l.Lock()
	defer l.Unlock()

	state, exists := l.counter[address]
	if !exists {
		return ErrCannotInitialize
	}

	// Check if the validator has too many pending requests
	if state.pending >= l.maxPendingRequests {
		return ErrLimitReached
	}

	state.pending++
	state.TotalProposed++

	return nil
}

// Decrement decrements counter for address in round.
//
// If the address is not registered or has zero pending requests, the call is ineffectual.
func (l *Limiter) Decrement(address common.Address) {
	l.Lock()
	defer l.Unlock()

	state, exists := l.counter[address]
	if !exists {
		return
	}

	if state.pending > 0 {
		state.pending--
	}

	state.TotalCompleted++
}
