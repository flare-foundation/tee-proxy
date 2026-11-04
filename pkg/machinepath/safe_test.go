package machinepath

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/testutil"
)

// approvalFixture is execTransaction evidence approving the sampleHash list
// signed by two of the three test owners at safeNonce.
type approvalFixture struct {
	inputs      safe.ExecTransactionInputs
	calldata    []byte
	gov         types.Governance
	manager     common.Address
	extensionID common.Hash
	messageHash common.Hash
}

const (
	fixtureChainID   = uint64(14)
	fixtureListNonce = uint64(7)
	fixtureSafeNonce = uint64(3)
)

func newApprovalFixture(t *testing.T) approvalFixture {
	t.Helper()

	safeAddress := common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")

	_, extensionID := sampleHash(t)
	messageHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000abcdef01")
	calldata, err := types.ApproveMachinePathListCalldata(extensionID, fixtureListNonce, messageHash)
	require.NoError(t, err)

	tx := safe.Tx{To: manager, Data: calldata, Operation: safe.OperationCall, Nonce: new(big.Int).SetUint64(fixtureSafeNonce)}
	txHash, err := tx.Hash(fixtureChainID, safeAddress)
	require.NoError(t, err)

	return approvalFixture{
		inputs: safe.ExecTransactionInputs{
			To: manager, Data: calldata, Operation: safe.OperationCall,
			Signatures: testutil.SignSafeTx(t, txHash, testutil.SafeOwnerKeys[:2]),
		},
		calldata:    calldata,
		gov:         types.Governance{Safe: safeAddress, Signers: testutil.OwnerAddresses(t, testutil.SafeOwnerKeys), Threshold: 2},
		manager:     manager,
		extensionID: extensionID,
		messageHash: messageHash,
	}
}

// TestMatchApprovalAndVerify exercises the proxy-side selection contract: an
// execTransaction whose inner call approves the machine path list is matched,
// and its evidence verifies with exactly the check the TEE node applies at the
// Safe nonce carried by the event.
func TestMatchApprovalAndVerify(t *testing.T) {
	fx := newApprovalFixture(t)

	require.True(t, matchApproval(fx.inputs, fx.calldata, fx.manager))

	// The node verifies at the nonce the proxy takes from the event.
	approval := safe.Approval{ExecTransaction: fx.inputs, Nonce: fixtureSafeNonce}
	require.NoError(t, approval.Verify(fixtureChainID, fx.gov.Safe, fx.gov.Signers, fx.gov.Threshold))

	// A wrong nonce fails verification.
	bad := safe.Approval{ExecTransaction: fx.inputs, Nonce: fixtureSafeNonce + 1}
	require.ErrorIs(t, bad.Verify(fixtureChainID, fx.gov.Safe, fx.gov.Signers, fx.gov.Threshold), safe.ErrThresholdNotMet)
}

func TestMatchApprovalRejectsMismatches(t *testing.T) {
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	other := common.HexToAddress("0x00000000000000000000000000000000deadbeef")
	expected := []byte{0xaa, 0xbb}

	// Wrong target contract.
	require.False(t, matchApproval(safe.ExecTransactionInputs{To: other, Data: expected, Operation: safe.OperationCall}, expected, manager))

	// DELEGATECALL instead of CALL.
	require.False(t, matchApproval(safe.ExecTransactionInputs{To: manager, Data: expected, Operation: 1}, expected, manager))

	// Different inner calldata.
	require.False(t, matchApproval(safe.ExecTransactionInputs{To: manager, Data: []byte{0xaa, 0xcc}, Operation: safe.OperationCall}, expected, manager))
}

// TestSelectApprovalSkipsUnverifiable returns the first candidate that matches
// the list and verifies, and nil when none does.
func TestSelectApprovalSkipsUnverifiable(t *testing.T) {
	fx := newApprovalFixture(t)

	wrongNonce := safe.Approval{ExecTransaction: fx.inputs, Nonce: fixtureSafeNonce + 1}
	otherList := safe.Approval{ExecTransaction: safe.ExecTransactionInputs{To: fx.manager, Data: []byte{0x01}, Operation: safe.OperationCall}, Nonce: fixtureSafeNonce}
	good := safe.Approval{ExecTransaction: fx.inputs, Nonce: fixtureSafeNonce}

	got, err := selectApproval([]safe.Approval{wrongNonce, otherList, good}, fx.gov, fx.manager, fx.extensionID, fixtureListNonce, fx.messageHash, fixtureChainID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, fixtureSafeNonce, got.Nonce)

	got, err = selectApproval([]safe.Approval{wrongNonce, otherList}, fx.gov, fx.manager, fx.extensionID, fixtureListNonce, fx.messageHash, fixtureChainID)
	require.NoError(t, err)
	require.Nil(t, got)
}
