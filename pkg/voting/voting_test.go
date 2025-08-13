package voting

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestH(t *testing.T) {
	r := Receipt{
		InstructionHash:               common.Hash{},
		Sequence:                      0,
		Signature:                     hexutil.Bytes{},
		AdditionalVariableMessageHash: common.Hash{},
		Timestamp:                     0,
		VoteHash:                      common.Hash{},
	}

	h, err := r.Hash()
	require.NoError(t, err)
	require.NotEqual(t, common.Hash{}, h)

	pk, err := crypto.GenerateKey()
	require.NoError(t, err)

	sr, err := r.Sign(pk)
	require.NoError(t, err)

	require.Equal(t, r, sr.Receipt)

	pub, err := sr.RecoverPubKey()
	require.NoError(t, err)

	require.Equal(t, pk.PublicKey, *pub)
}
