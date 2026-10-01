package voting

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/fdc2"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-node/pkg/fdc"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

const voteTestChainID uint64 = 14

type testMeta struct{}

func (*testMeta) Cosigners(_ *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	return map[common.Address]bool{}, 0, nil
}

func (*testMeta) CheckConsistency(_ *instruction.Data, _ common.Address) error {
	return nil
}

func (*testMeta) ThresholdBIPS(_ *instruction.DataFixed) (int, error) {
	return -1, nil
}

// bipsMeta reports a fixed thresholdBIPS; -1 means no override.
type bipsMeta struct{ bips int }

func (*bipsMeta) Cosigners(_ *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	return map[common.Address]bool{}, 0, nil
}

func (*bipsMeta) CheckConsistency(_ *instruction.Data, _ common.Address) error {
	return nil
}

func (m *bipsMeta) ThresholdBIPS(_ *instruction.DataFixed) (int, error) {
	return m.bips, nil
}

// policyWithThreshold builds a signing policy with an explicit threshold;
// testutil.GeneratePolicy always derives one from the weights.
func policyWithThreshold(t *testing.T, weights []uint16, threshold uint16) *cpolicy.SigningPolicy {
	t.Helper()

	voters := make([]common.Address, len(weights))
	for i := range weights {
		voters[i] = common.BigToAddress(big.NewInt(int64(i + 1)))
	}

	p, err := cpolicy.NewSigningPolicy(&relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(1),
		StartVotingRoundId: 0,
		Threshold:          threshold,
		Seed:               big.NewInt(2),
		Voters:             voters,
		Weights:            weights,
		SigningPolicyBytes: []byte{},
	}, nil)
	require.NoError(t, err)

	return p
}

func TestBuildVoteBoxThreshold(t *testing.T) {
	data := &instruction.Data{DataFixed: instruction.DataFixed{
		OPType:    op.XRP.Hash(),
		OPCommand: op.Pay.Hash(),
		Timestamp: uint64(time.Now().Unix()),
	}}
	signer := common.BigToAddress(big.NewInt(1))
	weights := []uint16{50, 30, 20}

	// 55 is neither floor nor ceil of half the total weight, so a recomputed
	// threshold cannot be mistaken for the policy's own
	t.Run("no override reads the policy threshold", func(t *testing.T) {
		round := createRound(policyWithThreshold(t, weights, 55), 10, false)

		box, err := buildVoteBox(data, signer, round, &bipsMeta{-1}, time.Minute)
		require.NoError(t, err)
		require.Equal(t, uint16(55), box.proposal.threshold)
	})

	t.Run("a bips override does not consult the policy", func(t *testing.T) {
		round := createRound(policyWithThreshold(t, weights, 0), 10, false)

		box, err := buildVoteBox(data, signer, round, &bipsMeta{6000}, time.Minute)
		require.NoError(t, err)
		require.Equal(t, uint16(60), box.proposal.threshold)
	})

	t.Run("a zero policy threshold is rejected", func(t *testing.T) {
		round := createRound(policyWithThreshold(t, weights, 0), 10, false)

		_, err := buildVoteBox(data, signer, round, &bipsMeta{-1}, time.Minute)
		require.ErrorIs(t, err, errZeroPolicyThreshold)
	})
}

func TestStorage(t *testing.T) {
	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 10,
	}, &testMeta{}, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	_, ok := s.Get(1)
	require.True(t, ok)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := i.HashForSigning(voteTestChainID)
	require.NoError(t, err)

	a1 := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)
	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	a2 := crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey)
	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	sk, err := crypto.GenerateKey()
	require.NoError(t, err)
	af := crypto.PubkeyToAddress(sk.PublicKey)
	sf, err := instruction.SignInstructionHash(h, sk)
	require.NoError(t, err)

	r1, err := s.AddVote(i, a1, s1)
	require.NoError(t, err)
	require.Equal(t, uint64(0), r1.Sequence)

	hf, err := i.HashFixed()
	require.NoError(t, err)
	require.Equal(t, hf, r1.InstructionHash)

	r2, err := s.AddVote(i, a2, s2)
	require.NoError(t, err)
	require.Equal(t, uint64(1), r2.Sequence)

	_, err = s.AddVote(i, af, sf)
	require.Error(t, err)

	a := <-s.Out

	require.Equal(t, a.Data.ID, i.InstructionID)

	require.Len(t, a.Signatures, 2)

	require.Contains(t, a.Signatures, hexutil.Bytes(s1))
	require.Contains(t, a.Signatures, hexutil.Bytes(s2))

	require.Equal(t, uint32(1), s.MaxConsensusEpoch(), "consensus watermark tracks the finalized voting's reward epoch")
}

func TestMaxConsensusEpoch(t *testing.T) {
	s := NewStorage(t.Context(), &config.Voting{HistorySize: 3, FinalizedBufferSize: 1}, &testMeta{}, nil)

	require.Equal(t, uint32(0), s.MaxConsensusEpoch(), "zero before any finalization")

	s.recordConsensusEpoch(5)
	require.Equal(t, uint32(5), s.MaxConsensusEpoch())

	s.recordConsensusEpoch(3) // a late finalization for an older epoch must not lower the watermark
	require.Equal(t, uint32(5), s.MaxConsensusEpoch())

	s.recordConsensusEpoch(6)
	require.Equal(t, uint32(6), s.MaxConsensusEpoch())
}

func TestFDCMessageValidity(t *testing.T) {
	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{}, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	chainID := uint64(14)

	_, ok := s.Get(1)
	require.True(t, ok)

	fdcReq := fdc2.IFdc2HubFdc2AttestationRequest{
		Header: fdc2.IFdc2HubFdc2RequestHeader{
			ThresholdBIPS:   5000,
			SourceId:        crypto.Keccak256Hash([]byte("todo")),
			AttestationType: crypto.Keccak256Hash([]byte("todo")),
		},
		RequestBody: []byte("TODO"),
	}
	fdcReqBytes, err := fdc.EncodeRequest(fdcReq)
	require.NoError(t, err)
	cosigners := []common.Address{crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)}
	cosignersThreshold := uint64(1)
	responseBody := crypto.Keccak256Hash([]byte("todo"))
	msgHash, _, err := fdc.HashMessage(chainID, fdcReq, responseBody[:], cosigners, cosignersThreshold, uint64(0))
	require.NoError(t, err)

	signature, err := teeutils.Sign(msgHash[:], testutil.PrivKey1)
	require.NoError(t, err)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              0,
			RewardEpochID:          1,
			OPType:                 op.FDC2.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        fdcReqBytes,
			AdditionalFixedMessage: responseBody[:],
			Cosigners:              cosigners,
			CosignersThreshold:     cosignersThreshold,
		},
		AdditionalVariableMessage: signature,
	}

	err = s.meta.CheckConsistency(i, i.Cosigners[0])
	require.NoError(t, err)
}

func TestFDCMessage(t *testing.T) {
	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{}, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	chainID := uint64(14)

	_, ok := s.Get(1)
	require.True(t, ok)

	fdcReq := fdc2.IFdc2HubFdc2AttestationRequest{
		Header: fdc2.IFdc2HubFdc2RequestHeader{
			ThresholdBIPS:   5000,
			SourceId:        crypto.Keccak256Hash([]byte("todo")),
			AttestationType: crypto.Keccak256Hash([]byte("todo")),
		},
		RequestBody: []byte("TODO"),
	}
	fdcReqBytes, err := fdc.EncodeRequest(fdcReq)
	require.NoError(t, err)
	cosigners := []common.Address{crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)}
	cosignersThreshold := uint64(1)
	responseBody := crypto.Keccak256Hash([]byte("todo"))
	msgHash, _, err := fdc.HashMessage(chainID, fdcReq, responseBody[:], cosigners, cosignersThreshold, uint64(0))
	require.NoError(t, err)

	signature, err := teeutils.Sign(msgHash[:], testutil.PrivKey1)
	require.NoError(t, err)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.FDC2.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        fdcReqBytes,
			AdditionalFixedMessage: responseBody[:],
			Cosigners:              cosigners,
			CosignersThreshold:     cosignersThreshold,
		},
		AdditionalVariableMessage: signature,
	}

	rec, err := s.AddVote(i, cosigners[0], signature)
	require.NoError(t, err)

	hash, err := i.HashFixed()
	require.NoError(t, err)

	require.Equal(t, rec.InstructionHash, hash)
	require.Equal(t, rec.AdditionalVariableMessageHash, crypto.Keccak256Hash(i.AdditionalVariableMessage))
}

func TestStorageConcurrent(t *testing.T) {
	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{}, nil)

	s.StoreNewRound(testutil.TestSigningPolicy)

	_, ok := s.Get(1)
	require.True(t, ok)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              0,
			RewardEpochID:          1,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := i.HashForSigning(voteTestChainID)
	require.NoError(t, err)

	a1 := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)
	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	a2 := crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey)
	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	af := crypto.PubkeyToAddress(privKey.PublicKey)
	sf, err := instruction.SignInstructionHash(h, privKey)
	require.NoError(t, err)

	go func() {
		var _, err1 = s.AddVote(i, a1, s1)
		require.NoError(t, err1)
	}()
	go func() {
		var _, err2 = s.AddVote(i, a2, s2)
		require.NoError(t, err2)
	}()
	go func() {
		var _, err3 = s.AddVote(i, af, sf)
		require.Error(t, err3)
	}()

	a := <-s.Out

	require.Equal(t, a.Data.ID, i.InstructionID, "ids")

	require.Len(t, a.Signatures, 2, "no of signatures")

	require.Contains(t, a.Signatures, hexutil.Bytes(s1), "sig1")
	require.Contains(t, a.Signatures, hexutil.Bytes(s2), "sig2")
}

func TestAddingVoteAfterExpiry(t *testing.T) {
	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  100 * time.Millisecond,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{}, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	_, ok := s.Get(1)
	require.True(t, ok)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := i.HashForSigning(voteTestChainID)
	require.NoError(t, err)

	a1 := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)
	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	a2 := crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey)
	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	r1, err := s.AddVote(i, a1, s1)
	require.NoError(t, err)
	require.Equal(t, uint64(0), r1.Sequence)

	// Wait for the proposal to expire (ProposalExpiration is 100ms in this test's config).
	// Not replaced with polling: repeated AddVote calls would change semantics, and this
	// sleep is waiting on a pure time-based trigger, not a pollable condition.
	time.Sleep(100 * time.Millisecond)

	_, err = s.AddVote(i, a2, s2)
	require.Error(t, err)
}

// TestAddVoteFinalizedSendCancelledOnShutdown verifies that a finalizing vote does not block
// forever on the finalized-action channel when the consumer is not draining it: once the
// service context is cancelled, AddVote abandons the send and returns an error instead of parking.
func TestAddVoteFinalizedSendCancelledOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	s := NewStorage(ctx, &config.Voting{
		ProposalExpiration:  10 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 1,
	}, &testMeta{}, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	// Fill the finalized-action buffer so the finalizing send below cannot proceed.
	s.Out <- &types.Action{}

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("shutdown-send")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := i.HashForSigning(voteTestChainID)
	require.NoError(t, err)

	a1 := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)
	sig1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	a2 := crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey)
	sig2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	// First vote is below threshold: it records but emits no action.
	_, err = s.AddVote(i, a1, sig1)
	require.NoError(t, err)

	cancel()

	// The second vote finalizes and tries to emit the threshold action, but the buffer is full
	// and the context is cancelled, so AddVote must return promptly rather than block.
	done := make(chan error, 1)
	go func() {
		_, err := s.AddVote(i, a2, sig2)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("AddVote blocked on a full finalized-action channel after shutdown")
	}
}

// TestConcurrentVoteAtExpiry verifies AddVote and scheduleEnd respect a consistent
// lock order (boxes → box) under concurrent expiry.
func TestConcurrentVoteAtExpiry(t *testing.T) {
	const expiry = 50 * time.Millisecond

	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  expiry,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{}, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("concurrent-expiry")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := i.HashForSigning(voteTestChainID)
	require.NoError(t, err)

	a1 := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)
	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	a2 := crypto.PubkeyToAddress(testutil.PrivKey2.PublicKey)
	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	_, err = s.AddVote(i, a1, s1)
	require.NoError(t, err)

	// Race a second vote with scheduleEnd firing.
	done := make(chan struct{})
	go func() {
		time.Sleep(expiry)
		_, _ = s.AddVote(i, a2, s2)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: AddVote did not complete within 2s of expiry")
	}
}

func TestComputeThreshold(t *testing.T) {
	tests := []struct {
		totalWeight uint16
		bips        int
		threshold   uint16
	}{
		{totalWeight: 0, bips: 0, threshold: 0},
		{totalWeight: 100, bips: 0, threshold: 0},
		{totalWeight: 10000, bips: 1, threshold: 1},
		// floors on remainder, matching Relay.sol's div(mul(total, bips), 10000)
		{totalWeight: 1, bips: 1, threshold: 0},
		{totalWeight: 123, bips: 10000, threshold: 123},
		{totalWeight: 65491, bips: 5000, threshold: 32745},
		{totalWeight: 65493, bips: 5000, threshold: 32746},
		{totalWeight: 65498, bips: 5000, threshold: 32749},
		{totalWeight: 65496, bips: 5000, threshold: 32748},
	}

	for j, test := range tests {
		require.Equal(t, test.threshold, computeThreshold(test.totalWeight, test.bips), j)
	}
}
