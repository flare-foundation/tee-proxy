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

	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

// fixedThresholdMeta returns a caller-chosen cosigner set and threshold, so a test can
// drive an out-of-range cosigner threshold into the box.
type fixedThresholdMeta struct {
	cosigners          map[common.Address]bool
	cosignersThreshold uint64
}

func (*fixedThresholdMeta) ThresholdBIPS(_ *instruction.DataFixed) (int, error) { return -1, nil }

func (m *fixedThresholdMeta) Cosigners(_ *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	return m.cosigners, m.cosignersThreshold, nil
}

func (*fixedThresholdMeta) CheckConsistency(_ *instruction.Data, _ common.Address) error { return nil }

// TestCosignerThresholdExceedingSetIsRejected guards the cosigner-threshold bound: a
// declared threshold of 65536 truncated to uint16 becomes 0 and would finalize a box with
// no cosigners. A threshold larger than the cosigner set must be rejected at box open.
func TestCosignerThresholdExceedingSetIsRejected(t *testing.T) {
	m := &fixedThresholdMeta{
		cosigners:          map[common.Address]bool{},
		cosignersThreshold: 1 << 16, // 65536 -> uint16 wraps to 0
	}

	s := NewStorage(t.Context(), &config.Voting{
		ProposalExpiration:  2 * time.Second,
		MaxPendingRequests:  10,
		HistorySize:         3,
		FinalizedBufferSize: 3,
	}, m, nil)
	s.StoreNewRound(testutil.TestSigningPolicy)

	provider := crypto.PubkeyToAddress(testutil.PrivKey1.PublicKey)
	data := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("f08")),
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

	_, err := s.AddVote(data, provider, []byte{1})
	require.Error(t, err, "a cosigner threshold exceeding the cosigner set must be rejected")
}
