package limiter

import (
	"bytes"
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

func TestLimiterTopPending(t *testing.T) {
	a := common.HexToAddress("0x1")
	b := common.HexToAddress("0x2")
	c := common.HexToAddress("0x3")
	d := common.HexToAddress("0x4")
	l := New([]common.Address{a, b, c, d}, 10)

	require.Empty(t, l.TopPending(3), "no open proposals yet")

	// pending: b=3, c=2, a=1, d=0
	for range 3 {
		require.NoError(t, l.Increment(b))
	}
	for range 2 {
		require.NoError(t, l.Increment(c))
	}
	require.NoError(t, l.Increment(a))

	top := l.TopPending(3)
	require.Len(t, top, 3, "d has zero pending and must be excluded")
	require.Equal(t, b, top[0].Address)
	require.Equal(t, uint(3), top[0].Pending)
	require.Equal(t, c, top[1].Address)
	require.Equal(t, uint(2), top[1].Pending)
	require.Equal(t, a, top[2].Address)
	require.Equal(t, uint(1), top[2].Pending)

	require.Len(t, l.TopPending(2), 2, "n caps the result")

	l.Decrement(a)
	for _, vp := range l.TopPending(10) {
		require.NotEqual(t, a, vp.Address, "a dropped back to zero and must be excluded")
	}
}

func TestLimiterTopPendingTieBreak(t *testing.T) {
	lo := common.HexToAddress("0x5")
	hi := common.HexToAddress("0x6")
	require.Negative(t, bytes.Compare(lo.Bytes(), hi.Bytes()), "test premise: lo sorts before hi")

	// Register hi first so the result cannot rely on map/insertion order.
	l := New([]common.Address{hi, lo}, 10)
	for range 2 {
		require.NoError(t, l.Increment(lo))
		require.NoError(t, l.Increment(hi))
	}

	// Equal pending must order by ascending address, deterministically.
	top := l.TopPending(2)
	require.Len(t, top, 2)
	require.Equal(t, lo, top[0].Address, "lower address wins the tie")
	require.Equal(t, hi, top[1].Address)

	// At the tie boundary, the cap retains the lower address.
	capped := l.TopPending(1)
	require.Len(t, capped, 1)
	require.Equal(t, lo, capped[0].Address)
}
