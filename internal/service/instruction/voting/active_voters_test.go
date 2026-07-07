package voting

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

// TestRoundMarkProviderVoterConcurrent guards the votersMu synchronization the per-epoch
// participant gauges rely on: ProviderVoterCount is read on the scrape goroutine while
// markProviderVoter writes on request goroutines. Dropping the lock would surface under -race.
func TestRoundMarkProviderVoterConcurrent(t *testing.T) {
	r := createRound(testutil.TestSigningPolicy, 1000, true)

	const writers = 16
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				_ = r.ProviderVoterCount()
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		go func(i int) {
			defer wg.Done()
			r.markProviderVoter(common.BigToAddress(big.NewInt(int64(i))))
		}(i)
	}
	wg.Wait()
	close(done)

	require.Equal(t, writers, r.ProviderVoterCount())
}

func TestRoundMarkProviderVoterAndProposerDistinct(t *testing.T) {
	r := createRound(testutil.TestSigningPolicy, 10, true)
	require.Zero(t, r.ProviderVoterCount())
	require.Zero(t, r.ProposerCount())

	a := common.HexToAddress("0x1")
	r.markProviderVoter(a)
	r.markProviderVoter(a) // idempotent
	r.markProviderVoter(common.HexToAddress("0x2"))
	require.Equal(t, 2, r.ProviderVoterCount())

	r.markProposer(a)
	r.markProposer(a) // idempotent
	require.Equal(t, 1, r.ProposerCount())
}

func TestRoundNotCollectingParticipantsStaysZero(t *testing.T) {
	r := createRound(testutil.TestSigningPolicy, 10, false)
	r.markProviderVoter(common.HexToAddress("0x1"))
	r.markProposer(common.HexToAddress("0x1"))

	require.Zero(t, r.ProviderVoterCount(), "data-provider voters must not be tracked when collection is off")
	require.Zero(t, r.ProposerCount(), "initiators must not be tracked when collection is off")
}

func TestCurrentRoundParticipantCounts(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true})
	s := newTestStorage(t, m)

	require.Zero(t, s.CurrentRoundProviderVoterCount(), "no round stored yet")
	require.Zero(t, s.CurrentRoundInitiatorCount())
	require.Empty(t, s.CurrentRoundTopPending(3))

	s.StoreNewRound(testutil.TestSigningPolicy)
	r, ok := s.Get(testutil.TestSigningPolicy.RewardEpochID)
	require.True(t, ok)

	r.markProviderVoter(common.HexToAddress("0x1"))
	r.markProposer(common.HexToAddress("0x2"))
	require.Equal(t, 1, s.CurrentRoundProviderVoterCount())
	require.Equal(t, 1, s.CurrentRoundInitiatorCount())

	// Two open proposals from one voter surface as that voter's pending count.
	addr := common.HexToAddress("0x9")
	r.limiter.Add(addr)
	require.NoError(t, r.limiter.Increment(addr))
	require.NoError(t, r.limiter.Increment(addr))

	top := s.CurrentRoundTopPending(3)
	require.Len(t, top, 1)
	require.Equal(t, addr, top[0].Address)
	require.Equal(t, uint(2), top[0].Pending)
}

func TestOldestStoredEpoch(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true})
	s := newTestStorage(t, m)

	_, ok := s.OldestStoredEpoch()
	require.False(t, ok, "no rounds stored yet")

	for _, e := range []uint32{100, 101, 102} {
		s.StoreNewRound(policyAtEpoch(e))
	}
	oldest, ok := s.OldestStoredEpoch()
	require.True(t, ok)
	require.Equal(t, uint32(100), oldest)

	// A fourth epoch evicts the oldest from the size-3 cyclic buffer.
	s.StoreNewRound(policyAtEpoch(103))
	oldest, ok = s.OldestStoredEpoch()
	require.True(t, ok)
	require.Equal(t, uint32(101), oldest)
}

// newTestStorage builds a voting Storage with a size-3 history for participant/epoch tests.
func newTestStorage(t *testing.T, m *metrics.Metrics) *Storage {
	t.Helper()
	return NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 10,
	}, &testMeta{}, m)
}

// policyAtEpoch returns the test signing policy rebased onto reward epoch e.
func policyAtEpoch(e uint32) *policy.SigningPolicy {
	p := *testutil.TestSigningPolicy
	p.RewardEpochID = e
	return &p
}
