package voting

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/connector"
	"github.com/flare-foundation/tee-node/pkg/ftdc"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/stretchr/testify/require"

	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

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

func TestStorage(t *testing.T) {
	s := NewStorage(&config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 10,
	}, &testMeta{})
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

	h, err := i.HashForSigning()
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
}

func TestFTDCMessageValidity(t *testing.T) {
	s := NewStorage(&config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{})
	s.StoreNewRound(testutil.TestSigningPolicy)

	_, ok := s.Get(1)
	require.True(t, ok)

	ftdcReq := connector.IFtdcHubFtdcAttestationRequest{
		Header: connector.IFtdcHubFtdcRequestHeader{
			ThresholdBIPS:   5000,
			SourceId:        crypto.Keccak256Hash([]byte("todo")),
			AttestationType: crypto.Keccak256Hash([]byte("todo")),
		},
		RequestBody: []byte("TODO"),
	}
	ftdcReqBytes, err := ftdc.EncodeRequest(ftdcReq)
	require.NoError(t, err)
	cosigners := []common.Address{crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)}
	cosignersThreshold := uint64(1)
	responseBody := crypto.Keccak256Hash([]byte("todo"))
	msgHash, _, _, err := ftdc.HashMessage(ftdcReq, responseBody[:], cosigners, cosignersThreshold, uint64(0))
	require.NoError(t, err)

	signature, err := teeutils.Sign(msgHash[:], testutil.PrivKey1)
	require.NoError(t, err)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              0,
			RewardEpochID:          1,
			OPType:                 op.FTDC.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        ftdcReqBytes,
			AdditionalFixedMessage: responseBody[:],
			Cosigners:              cosigners,
			CosignersThreshold:     cosignersThreshold,
		},
		AdditionalVariableMessage: signature,
	}

	err = s.meta.CheckConsistency(i, i.Cosigners[0])
	require.NoError(t, err)
}

func TestFTDCMessage(t *testing.T) {
	s := NewStorage(&config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{})
	s.StoreNewRound(testutil.TestSigningPolicy)

	_, ok := s.Get(1)
	require.True(t, ok)

	ftdcReq := connector.IFtdcHubFtdcAttestationRequest{
		Header: connector.IFtdcHubFtdcRequestHeader{
			ThresholdBIPS:   5000,
			SourceId:        crypto.Keccak256Hash([]byte("todo")),
			AttestationType: crypto.Keccak256Hash([]byte("todo")),
		},
		RequestBody: []byte("TODO"),
	}
	ftdcReqBytes, err := ftdc.EncodeRequest(ftdcReq)
	require.NoError(t, err)
	cosigners := []common.Address{crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)}
	cosignersThreshold := uint64(1)
	responseBody := crypto.Keccak256Hash([]byte("todo"))
	msgHash, _, _, err := ftdc.HashMessage(ftdcReq, responseBody[:], cosigners, cosignersThreshold, uint64(0))
	require.NoError(t, err)

	signature, err := teeutils.Sign(msgHash[:], testutil.PrivKey1)
	require.NoError(t, err)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.FTDC.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        ftdcReqBytes,
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
	s := NewStorage(&config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{})

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

	h, err := i.HashForSigning()
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
	s := NewStorage(&config.Voting{
		ProposalExpiration:  100 * time.Millisecond,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, &testMeta{})
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

	h, err := i.HashForSigning()
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

	time.Sleep(100 * time.Millisecond)

	_, err = s.AddVote(i, a2, s2)
	require.Error(t, err)
}

func TestComputeThresholdSigningPolicy(t *testing.T) {
	tests := []struct {
		totalWeight uint16
		threshold   uint16
	}{
		{
			totalWeight: 0,
			threshold:   0,
		},
		{
			totalWeight: 65491,
			threshold:   32746,
		},
		{
			totalWeight: 65493,
			threshold:   32747,
		},
		{
			totalWeight: 65498,
			threshold:   32749,
		},
		{
			totalWeight: 65496,
			threshold:   32748,
		},
	}

	for j, test := range tests {
		require.Equal(t, test.threshold, computeThreshold(test.totalWeight, 5000), j)
	}
}

func TestComputeThresholdCustom(t *testing.T) {
	tests := []struct {
		totalWeight uint16
		bips        int
		threshold   uint16
	}{
		{
			totalWeight: 0,
			bips:        0,
			threshold:   0,
		},
		{
			totalWeight: 100,
			bips:        0,
			threshold:   0,
		},
		{
			totalWeight: 10000,
			bips:        1,
			threshold:   1,
		},
		{
			totalWeight: 1,
			bips:        1,
			threshold:   1,
		},
		{
			totalWeight: 123,
			bips:        10000,
			threshold:   123,
		},
	}

	for j, test := range tests {
		require.Equal(t, test.threshold, computeThreshold(test.totalWeight, test.bips), j)
	}
}
