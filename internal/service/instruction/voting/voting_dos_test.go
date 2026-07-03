package voting

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-proxy/internal/service/instruction/voting/limiter"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

// TestCheckSizeRejectsOversizedCosigners guards the cosigner-count bound: checkSize must
// reject an oversized Cosigners slice before it is map-allocated and ABI-encoded.
func TestCheckSizeRejectsOversizedCosigners(t *testing.T) {
	data := &instruction.Data{
		DataFixed: instruction.DataFixed{
			OPCommand: op.KeyGenerate.Hash(),
			// Far above any real admin/cosigner set.
			Cosigners: make([]common.Address, 5000),
		},
	}

	err := checkSize(data, 0)
	require.Error(t, err, "an absurdly large cosigner list must be rejected pre-lock")
}

// spyMeta records whether the expensive per-box metadata resolution ran.
type spyMeta struct {
	metaTouched bool
}

func (m *spyMeta) ThresholdBIPS(_ *instruction.DataFixed) (int, error) {
	m.metaTouched = true
	return -1, nil
}

func (m *spyMeta) Cosigners(_ *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	m.metaTouched = true
	return map[common.Address]bool{}, 0, nil
}

func (m *spyMeta) CheckConsistency(_ *instruction.Data, _ common.Address) error {
	return nil
}

// TestIneligibleProposerRejectedBeforeMetaWork guards that a proposer outside the provider
// set is rejected before any threshold/cosigner metadata work runs: the spy meta must
// report it was never touched.
func TestIneligibleProposerRejectedBeforeMetaWork(t *testing.T) {
	m := &spyMeta{}
	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, m, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	// A freshly generated key is not in the test policy's voter set.
	sk, err := crypto.GenerateKey()
	require.NoError(t, err)
	ineligible := crypto.PubkeyToAddress(sk.PublicKey)

	data := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("f03-ineligible")),
			TeeID:                  common.HexToAddress("dead"),
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyGenerate.Hash(),
			OriginalMessage:        []byte("x"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	_, err = s.AddVote(data, ineligible, []byte{1})
	require.Error(t, err)
	require.ErrorIs(t, err, limiter.ErrCannotInitialize)
	require.False(t, m.metaTouched, "eligibility must be checked before any metadata work")
}
