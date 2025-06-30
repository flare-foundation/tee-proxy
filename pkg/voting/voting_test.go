package voting

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/stretchr/testify/require"
)

type testMeta struct{}

func (*testMeta) Cosigners(_ *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	return map[common.Address]bool{}, 0, nil
}

func (*testMeta) Threshold(_ *instruction.DataFixed) (int, error) {
	return -1, nil
}

func TestStorage(t *testing.T) {
	out := make(chan *types.Action, 3)

	s := NewStorage(3, &testMeta{}, out)

	s.CreateRound(testutil.TestSigningPolicy)

	_, ok := s.Get(1)
	require.True(t, ok)

	i := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("todo")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              0,
			RewardEpochID:          big.NewInt(1),
			OPType:                 constants.Wallet.Hash(),
			OPCommand:              constants.KeyGenerate.Hash(),
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

	r2, err := s.AddVote(i, a2, s2)
	require.NoError(t, err)
	require.Equal(t, uint64(1), r2.Sequence)

	a := <-out

	require.NotNil(t, a)
}
