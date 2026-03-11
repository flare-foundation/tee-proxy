package limiter

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestLimiter(t *testing.T) {
	limit := uint(3)

	voters := make([]common.Address, 0, 3)

	for j := range 3 {
		voters = append(voters, common.BytesToAddress(fmt.Appendf(nil, "address%d", j)))
	}

	l := New(voters[0:2], limit)

	err := l.Increment(voters[0])
	require.NoError(t, err)

	l.Decrement(voters[0])
	l.Decrement(voters[1])

	err = l.Increment(voters[2])
	require.Error(t, err)

	l.Decrement(voters[2])

	// reach limit
	for range limit {
		err := l.Increment(voters[0])
		require.NoError(t, err)
	}

	err = l.Increment(voters[0])
	require.Error(t, err)

	// adding existent voter has no effect
	l.Add(voters[0])
	err = l.Increment(voters[0])
	require.Error(t, err)

	// add new voter
	l.Add(voters[2])
	err = l.Increment(voters[2])
	require.NoError(t, err)
}
