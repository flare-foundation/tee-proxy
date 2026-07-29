package voting

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

const (
	// policyTotalWeight is testutil.TestSigningPolicy's total voter weight (1+3+3).
	policyTotalWeight = 7.0
	bipsDelta         = 1e-6
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
	// A real policy voter, so a weight leak would show up instead of being masked by a
	// VoterDataMap miss.
	r.markProviderVoter(crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey))
	r.markProposer(common.HexToAddress("0x1"))
	r.recordVotingWeight(3)

	require.Zero(t, r.ProviderVoterCount(), "data-provider voters must not be tracked when collection is off")
	require.Zero(t, r.ProposerCount(), "initiators must not be tracked when collection is off")
	require.Zero(t, r.VotedWeightBips(), "voted weight must not be tracked when collection is off")
	require.Zero(t, r.MaxVotingWeightBips(), "the voting-weight watermark must not be tracked when collection is off")
	// The threshold is policy-derived, so it is available regardless of collection.
	require.InDelta(t, maxBIPS*3/policyTotalWeight, r.ThresholdBips(), bipsDelta)
}

// TestRoundWeightBips uses the real policy voters (weights 1, 3, 3 of total 7): synthetic
// addresses miss VoterDataMap and would make every weight assertion pass vacuously.
func TestRoundWeightBips(t *testing.T) {
	r := createRound(testutil.TestSigningPolicy, 10, true)

	require.Zero(t, r.VotedWeightBips())
	require.Zero(t, r.MaxVotingWeightBips())
	require.InDelta(t, maxBIPS*3/policyTotalWeight, r.ThresholdBips(), bipsDelta)

	r.markProviderVoter(crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)) // weight 1
	require.InDelta(t, maxBIPS*1/policyTotalWeight, r.VotedWeightBips(), bipsDelta)

	r.markProviderVoter(crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey)) // weight 3
	r.markProviderVoter(crypto.PubkeyToAddress(testutil.PrivKey3.PublicKey)) // weight 3
	require.InDelta(t, float64(maxBIPS), r.VotedWeightBips(), bipsDelta, "the whole voter set sums to TotalWeight")

	r.markProviderVoter(common.HexToAddress("0xdead")) // outside the policy: carries no weight
	require.InDelta(t, float64(maxBIPS), r.VotedWeightBips(), bipsDelta)

	r.recordVotingWeight(3)
	r.recordVotingWeight(2) // the watermark is monotone
	require.InDelta(t, maxBIPS*3/policyTotalWeight, r.MaxVotingWeightBips(), bipsDelta)
}

func TestCurrentRoundParticipantCounts(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true})
	s := newTestStorage(t, m)

	require.Zero(t, s.CurrentRoundProviderVoterCount(), "no round stored yet")
	require.Zero(t, s.CurrentRoundInitiatorCount())
	require.Zero(t, s.CurrentVotedWeightBips())
	require.Zero(t, s.CurrentMaxVotingWeightBips())
	require.Zero(t, s.CurrentVotingThresholdBips())
	require.Empty(t, s.CurrentRoundTopPending(3))

	s.StoreNewRound(testutil.TestSigningPolicy)
	r, ok := s.Get(testutil.TestSigningPolicy.RewardEpochID)
	require.True(t, ok)

	r.markProviderVoter(common.HexToAddress("0x1"))
	r.markProposer(common.HexToAddress("0x2"))
	require.Equal(t, 1, s.CurrentRoundProviderVoterCount())
	require.Equal(t, 1, s.CurrentRoundInitiatorCount())
	require.InDelta(t, maxBIPS*3/policyTotalWeight, s.CurrentVotingThresholdBips(), bipsDelta)

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

// TestActiveVoterGaugesFollowReportedRound pins the scalar gauges' round selection: they report
// the newest resident round and fall back to its predecessor only while that round has no
// accepted provider votes (the policy-adoption overlap). Reporting the max across both rounds
// instead would mask a participation collapse for the whole epoch, since the predecessor stays
// resident and both values are monotone.
func TestActiveVoterGaugesFollowReportedRound(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true, Voting: true})
	s := newTestStorage(t, m)

	const epochN, epochNext = uint32(100), uint32(101)
	v1 := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey) // weight 1
	v2 := crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey) // weight 3
	v3 := crypto.PubkeyToAddress(testutil.PrivKey3.PublicKey) // weight 3

	s.StoreNewRound(policyAtEpoch(epochN))
	rN, ok := s.Get(epochN)
	require.True(t, ok)

	rN.markProviderVoter(v1)
	rN.markProviderVoter(v2)
	rN.markProviderVoter(v3)
	rN.markProposer(v1)
	rN.markProposer(v2)
	rN.recordVotingWeight(4)

	pending := common.HexToAddress("0x9")
	rN.limiter.Add(pending)
	require.NoError(t, rN.limiter.Increment(pending))
	require.NoError(t, rN.limiter.Increment(pending))

	require.Equal(t, 3, s.CurrentRoundProviderVoterCount())
	require.Equal(t, 2, s.CurrentRoundInitiatorCount())
	require.InDelta(t, float64(maxBIPS), s.CurrentVotedWeightBips(), bipsDelta)
	require.InDelta(t, maxBIPS*4/policyTotalWeight, s.CurrentMaxVotingWeightBips(), bipsDelta)
	require.InDelta(t, maxBIPS*3/policyTotalWeight, s.CurrentVotingThresholdBips(), bipsDelta)

	// A newly announced, empty round advances currentEpoch but must not zero the gauges.
	s.StoreNewRound(policyAtEpoch(epochNext))
	require.Equal(t, epochNext, s.currentEpoch.Load())

	require.Equal(t, 3, s.CurrentRoundProviderVoterCount(), "provider voters must survive the epoch overlap")
	require.Equal(t, 2, s.CurrentRoundInitiatorCount(), "initiators must survive the epoch overlap")
	require.InDelta(t, float64(maxBIPS), s.CurrentVotedWeightBips(), bipsDelta, "voted weight must survive the epoch overlap")
	require.InDelta(t, maxBIPS*4/policyTotalWeight, s.CurrentMaxVotingWeightBips(), bipsDelta)

	// Unfinalized proposals genuinely span the overlap, so top-pending keeps merging both rounds.
	top := s.CurrentRoundTopPending(3)
	require.Len(t, top, 1)
	require.Equal(t, pending, top[0].Address)
	require.Equal(t, uint(2), top[0].Pending)

	// The first accepted provider vote in the new round switches every scalar gauge to it, even
	// though the predecessor's values are higher — a collapse must not stay hidden.
	rNext, ok := s.Get(epochNext)
	require.True(t, ok)
	rNext.markProviderVoter(v1)

	require.Equal(t, 1, s.CurrentRoundProviderVoterCount(), "the newest round is reported, not the max")
	require.Zero(t, s.CurrentRoundInitiatorCount(), "all scalar gauges report the same round")
	require.InDelta(t, maxBIPS*1/policyTotalWeight, s.CurrentVotedWeightBips(), bipsDelta)
	require.Zero(t, s.CurrentMaxVotingWeightBips(), "no voting weight recorded in the newest round yet")
	require.InDelta(t, maxBIPS*3/policyTotalWeight, s.CurrentVotingThresholdBips(), bipsDelta)
}

// TestAddVoteRaisesWeightGauges covers the AddVote path end to end: an accepted provider vote
// must land both in the epoch's voted-weight sum and in the per-voting watermark.
func TestAddVoteRaisesWeightGauges(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, ActiveVoters: true, Voting: true})
	s := newTestStorage(t, m)
	s.StoreNewRound(testutil.TestSigningPolicy)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("weight")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          testutil.TestSigningPolicy.RewardEpochID,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("weight"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := i.HashForSigning(voteTestChainID)
	require.NoError(t, err)
	sig, err := instruction.SignInstructionHash(h, testutil.PrivKey1) // weight 1, below threshold 3
	require.NoError(t, err)

	_, err = s.AddVote(i, crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey), sig)
	require.NoError(t, err)

	require.InDelta(t, maxBIPS*1/policyTotalWeight, s.CurrentVotedWeightBips(), bipsDelta)
	require.InDelta(t, maxBIPS*1/policyTotalWeight, s.CurrentMaxVotingWeightBips(), bipsDelta)
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
