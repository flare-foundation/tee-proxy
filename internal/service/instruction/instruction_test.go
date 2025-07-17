package instruction

import (
	"math/big"
	"testing"
	"time"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
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

func TestVoting(t *testing.T) {
	mr := miniredis.RunT(t)
	c := queue.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	vs := voting.NewStorage(3, &testMeta{}, 3)
	vs.CreateRound(testutil.TestSigningPolicy)

	var teeId = common.HexToAddress("dead")

	PrivKey4, err := crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}

	as := queue.NewActionQueues(c)
	s := &Service{
		teeID:    teeId,
		vs:       vs,
		policies: make(chan policy.SigningPolicy, 1),
		aq:       as,
		pk:       PrivKey4,
	}

	go func() {
		err := s.Forward(t.Context())
		if err != nil {
			return
		}
	}()

	iData := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  teeId,
			Timestamp:              0,
			RewardEpochID:          big.NewInt(1),
			OPType:                 constants.Wallet.Hash(),
			OPCommand:              constants.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := iData.HashForSigning()
	require.NoError(t, err)

	//
	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	i1 := &instruction.Instruction{
		Data:      *iData,
		Signature: s1,
	}

	i2 := &instruction.Instruction{
		Data:      *iData,
		Signature: s2,
	}

	sr1, err := s.ServeInstruction(t.Context(), i1)
	require.NoError(t, err)
	require.Equal(t, uint64(0), sr1.Receipt.Sequence)

	sr2, err := s.ServeInstruction(t.Context(), i2)
	require.NoError(t, err)
	require.Equal(t, uint64(1), sr2.Receipt.Sequence)

	pubKey1, err := sr1.RecoverPubKey()
	require.NoError(t, err)
	require.True(t, pubKey1.Equal(&PrivKey4.PublicKey))

	pubKey2, err := sr2.RecoverPubKey()
	require.NoError(t, err)
	require.True(t, pubKey2.Equal(&PrivKey4.PublicKey))

	time.Sleep(500 * time.Millisecond)
	a, err := s.aq.GetAction(t.Context(), iData.InstructionID, types.Threshold)
	require.NoError(t, err)
	require.Equal(t, a.Data.ID, iData.InstructionID)

	require.Len(t, a.Signatures, 2)
	require.Contains(t, a.Signatures, hexutil.Bytes(s1))
	require.Contains(t, a.Signatures, hexutil.Bytes(s2))
}
