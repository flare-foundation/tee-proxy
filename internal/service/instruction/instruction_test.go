package instruction

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/policy"
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

	vCfg := &voting.Config{
		ProposalExpiration: 0,
		MaxPendingRequests: 0,
	}

	vs := voting.NewStorage(vCfg, 3, &testMeta{}, 3)
	vs.CreateRound(testutil.TestSigningPolicy)

	var teeId = common.HexToAddress("dead")

	PrivKey4, err := crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}

	aq := queue.NewActionQueues(c)
	s := &Service{
		teeID:    teeId,
		vs:       vs,
		policies: make(chan policy.SigningPolicy, 1),
		aq:       aq,
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
			InstructionId:          crypto.Keccak256Hash([]byte("todo")),
			TeeId:                  teeId,
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochId:          1,
			OpType:                 constants.Wallet.Hash(),
			OpCommand:              constants.KeyGenerate.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := iData.HashForSigning()
	require.NoError(t, err)

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
	require.True(t, pubKey1.X.Cmp(PrivKey4.X) == 0 && pubKey1.Y.Cmp(PrivKey4.Y) == 0)

	pubKey2, err := sr2.RecoverPubKey()
	require.NoError(t, err)
	require.True(t, pubKey2.X.Cmp(PrivKey4.X) == 0 && pubKey2.Y.Cmp(PrivKey4.Y) == 0)

	time.Sleep(2000 * time.Millisecond)
	a, err := s.aq.Pop(t.Context(), queue.Main)
	require.NoError(t, err)
	require.Equal(t, a.Data.ID, common.Hash(iData.InstructionId))

	require.Len(t, a.Signatures, 2)
	require.Contains(t, a.Signatures, hexutil.Bytes(s1))
	require.Contains(t, a.Signatures, hexutil.Bytes(s2))
}
