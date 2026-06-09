package voting

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestReceipt(t *testing.T) {
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

	sk, err := crypto.GenerateKey()
	require.NoError(t, err)

	const chainID = 31337

	sr, err := r.Sign(sk, chainID)
	require.NoError(t, err)

	require.Equal(t, r, sr.Receipt)

	pub, err := sr.RecoverPubKey(chainID)
	require.NoError(t, err)

	require.Equal(t, sk.PublicKey, *pub)
}
