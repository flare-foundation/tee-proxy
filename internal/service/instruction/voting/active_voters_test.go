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

// TestActiveVoterGaugesSurviveEpochOverlap guards the MA-11 fix: when a new signing policy is
// announced, its empty round advances currentEpoch, but the active-voter gauges must keep
// reporting the still-voting predecessor's counts (the max of the two resident rounds), not dip
// to zero. It also confirms the newer round wins once it accrues more participants.
func TestActiveVoterGaugesSurviveEpochOverlap(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true, Voting: true})
	s := newTestStorage(t, m)

	const epochN, epochNext = uint32(100), uint32(101)

	s.StoreNewRound(policyAtEpoch(epochN))
	rN, ok := s.Get(epochN)
	require.True(t, ok)

	rN.markProviderVoter(common.HexToAddress("0x1"))
	rN.markProviderVoter(common.HexToAddress("0x2"))
	rN.markProposer(common.HexToAddress("0x1"))

	pending := common.HexToAddress("0x9")
	rN.limiter.Add(pending)
	require.NoError(t, rN.limiter.Increment(pending))
	require.NoError(t, rN.limiter.Increment(pending))

	require.Equal(t, 2, s.CurrentRoundProviderVoterCount())
	require.Equal(t, 1, s.CurrentRoundInitiatorCount())

	// A newly announced, empty round advances currentEpoch but must not zero the gauges.
	s.StoreNewRound(policyAtEpoch(epochNext))
	require.Equal(t, epochNext, s.currentEpoch.Load())

	require.Equal(t, 2, s.CurrentRoundProviderVoterCount(), "provider voters must survive the epoch overlap")
	require.Equal(t, 1, s.CurrentRoundInitiatorCount(), "initiators must survive the epoch overlap")

	top := s.CurrentRoundTopPending(3)
	require.Len(t, top, 1)
	require.Equal(t, pending, top[0].Address)
	require.Equal(t, uint(2), top[0].Pending)

	// Once the newer round accrues MORE participants, the max follows it (not previous-only).
	rNext, ok := s.Get(epochNext)
	require.True(t, ok)
	for _, a := range []common.Address{
		common.HexToAddress("0xa"), common.HexToAddress("0xb"), common.HexToAddress("0xc"),
	} {
		rNext.markProviderVoter(a)
	}
	rNext.markProposer(common.HexToAddress("0xa"))
	rNext.markProposer(common.HexToAddress("0xb"))

	require.Equal(t, 3, s.CurrentRoundProviderVoterCount(), "newer round wins the max when it has more voters")
	require.Equal(t, 2, s.CurrentRoundInitiatorCount())
}

// TestTopPendingMergePerProviderMax guards that the cross-round top-pending merge takes the max
// per provider, not the sum, so a provider present in both resident rounds is not double-counted.
func TestTopPendingMergePerProviderMax(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true, Voting: true})
	s := newTestStorage(t, m)

	const epochN, epochNext = uint32(200), uint32(201)
	shared := common.HexToAddress("0x7")

	s.StoreNewRound(policyAtEpoch(epochN))
	rN, ok := s.Get(epochN)
	require.True(t, ok)
	rN.limiter.Add(shared)
	for range 3 {
		require.NoError(t, rN.limiter.Increment(shared)) // pending 3 in round N
	}

	s.StoreNewRound(policyAtEpoch(epochNext))
	rNext, ok := s.Get(epochNext)
	require.True(t, ok)
	rNext.limiter.Add(shared)
	require.NoError(t, rNext.limiter.Increment(shared)) // pending 1 in round N+1

	other := common.HexToAddress("0x8")
	rNext.limiter.Add(other)
	for range 2 {
		require.NoError(t, rNext.limiter.Increment(other)) // pending 2 in round N+1
	}

	top := s.CurrentRoundTopPending(3)
	require.Len(t, top, 2)
	// shared holds pending in both rounds; the merge reports max(3,1)=3, never the sum 4.
	require.Equal(t, shared, top[0].Address)
	require.Equal(t, uint(3), top[0].Pending)
	require.Equal(t, other, top[1].Address)
	require.Equal(t, uint(2), top[1].Pending)

	require.Len(t, s.CurrentRoundTopPending(1), 1, "n truncates the merged result")
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
