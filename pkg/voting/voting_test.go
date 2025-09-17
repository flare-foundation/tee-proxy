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

	sr, err := r.Sign(sk)
	require.NoError(t, err)

	require.Equal(t, r, sr.Receipt)

	pub, err := sr.RecoverPubKey()
	require.NoError(t, err)

	require.Equal(t, sk.PublicKey, *pub)
}

func TestConfig(t *testing.T) {
	tests := []struct {
		before Config
		after  Config
	}{
		{
			before: Config{},
			after: Config{
				ProposalExpiration: defaultProposalExpiration,
				MaxPendingRequests: defaultMaxPendingRequests,
			},
		},
		{
			before: Config{
				ProposalExpiration: 1,
				MaxPendingRequests: 1,
			},
			after: Config{
				ProposalExpiration: 1,
				MaxPendingRequests: 1,
			},
		},
		{
			before: Config{
				ProposalExpiration: -10,
				MaxPendingRequests: 1,
			},
			after: Config{
				ProposalExpiration: defaultProposalExpiration,
				MaxPendingRequests: 1,
			},
		},
		{
			before: Config{
				ProposalExpiration: 10,
				MaxPendingRequests: 0,
			},
			after: Config{
				ProposalExpiration: 10,
				MaxPendingRequests: defaultMaxPendingRequests,
			},
		},
	}

	for _, test := range tests {
		test.before.SetDefault()
		require.Equal(t, test.before, test.after)
	}
}
