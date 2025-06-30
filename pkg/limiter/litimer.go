package limiter

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

var unregisteredError = errors.New("voter not registered")
var limitReachedError = errors.New("propose limit reached")

type Limiter struct {
	counter map[common.Address]*State

	maxPendingRequests uint

	sync.RWMutex
}

// ValidatorState tracks the counts for a single validator.
type State struct {
	// Address common.Address
	pending uint
	// Note: The following two fields can be used to calculate the ratio of completed to proposed requests.
	// Note: This could be used to detect if a validator is malicious or just working incorrectly.
	TotalProposed  int
	TotalCompleted int
}

// New creates a new limiter that holds counters for <size> rounds and allows at mosts maxPendingRequests per voter.
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

// Increment increments counter for address in round and returns error if address is not eligible to vote.
func (l *Limiter) Increment(address common.Address) error {
	l.Lock()
	defer l.Unlock()

	state, exists := l.counter[address]
	if !exists {
		return fmt.Errorf("voter %v not registered", address)
	}

	// Check if the validator has too many pending requests
	if state.pending >= l.maxPendingRequests {
		return fmt.Errorf("voter %v has too may pending requests", address)
	}

	state.pending++
	state.TotalProposed++

	return nil
}

// Decrement decrements counter for address in round.
func (l *Limiter) Decrement(address common.Address) error {
	l.Lock()
	defer l.Unlock()

	state, exists := l.counter[address]
	if !exists {
		return fmt.Errorf("voter %v not registered in round", address)
	}

	if state.pending > 0 {
		state.pending--
	}

	state.TotalCompleted++

	return nil
}
