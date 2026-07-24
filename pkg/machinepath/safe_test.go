package machinepath

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// encodeExecTransactionHex encodes execTransaction calldata the way the
// indexer stores transaction input: hex without the 0x prefix.
func encodeExecTransactionHex(t *testing.T, in safe.ExecTransactionInputs) string {
	t.Helper()
	calldata, err := safe.EncodeExecTransactionCalldata(in)
	require.NoError(t, err)
	return hex.EncodeToString(calldata)
}

// signSafeTx returns the owner-signature blob over the Safe transaction hash
// (Safe v ∈ {27, 28}) for the given owner keys.
func signSafeTx(t *testing.T, txHash common.Hash, keys []string) []byte {
	t.Helper()
	blob := make([]byte, 0, len(keys)*safe.SignatureLength)
	for _, k := range keys {
		priv, err := crypto.HexToECDSA(k)
		require.NoError(t, err)
		raw, err := crypto.Sign(txHash[:], priv)
		require.NoError(t, err)
		raw[64] += 27
		blob = append(blob, raw...)
	}
	return blob
}

func ownerAddresses(t *testing.T, keys []string) []common.Address {
	t.Helper()
	addrs := make([]common.Address, len(keys))
	for i, k := range keys {
		priv, err := crypto.HexToECDSA(k)
		require.NoError(t, err)
		addrs[i] = crypto.PubkeyToAddress(priv.PublicKey)
	}
	return addrs
}

var testOwnerKeys = []string{
	"1111111111111111111111111111111111111111111111111111111111111111",
	"2222222222222222222222222222222222222222222222222222222222222222",
	"3333333333333333333333333333333333333333333333333333333333333333",
}

// TestMatchApprovalAndVerify exercises the proxy-side selection contract: an
// execTransaction whose inner call approves the machine path list is matched
// from indexed calldata, and its evidence verifies with exactly the check the
// TEE node applies at the Safe nonce carried by the event.
func TestMatchApprovalAndVerify(t *testing.T) {
	const (
		chainID   = uint64(14)
		listNonce = uint64(7)
		safeNonce = uint64(3)
		threshold = uint64(2)
	)
	safeAddress := common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")

	_, extensionID, _ := sampleHash(t)
	messageHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000abcdef01")
	calldata, err := types.ApproveMachinePathListCalldata(extensionID, listNonce, messageHash)
	require.NoError(t, err)

	owners := ownerAddresses(t, testOwnerKeys)

	tx := safe.Tx{To: manager, Data: calldata, Operation: safe.OperationCall, Nonce: new(big.Int).SetUint64(safeNonce)}
	txHash, err := tx.Hash(chainID, safeAddress)
	require.NoError(t, err)
	blob := signSafeTx(t, txHash, testOwnerKeys[:2])

	input := encodeExecTransactionHex(t, safe.ExecTransactionInputs{
		To: manager, Data: calldata, Operation: safe.OperationCall, Signatures: blob,
	})

	inputs, ok := matchApproval(input, calldata, manager)
	require.True(t, ok)

	// The node verifies at the nonce the proxy takes from the event.
	approval := safe.Approval{ExecTransaction: inputs, Nonce: safeNonce}
	require.NoError(t, approval.Verify(chainID, safeAddress, owners, threshold))

	// A wrong nonce fails verification.
	bad := safe.Approval{ExecTransaction: inputs, Nonce: safeNonce + 1}
	require.ErrorIs(t, bad.Verify(chainID, safeAddress, owners, threshold), safe.ErrThresholdNotMet)
}

func TestMatchApprovalRejectsMismatches(t *testing.T) {
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	other := common.HexToAddress("0x00000000000000000000000000000000deadbeef")
	expected := []byte{0xaa, 0xbb}

	// Wrong target contract.
	input := encodeExecTransactionHex(t, safe.ExecTransactionInputs{To: other, Data: expected, Operation: safe.OperationCall})
	_, ok := matchApproval(input, expected, manager)
	require.False(t, ok)

	// DELEGATECALL instead of CALL.
	input = encodeExecTransactionHex(t, safe.ExecTransactionInputs{To: manager, Data: expected, Operation: 1})
	_, ok = matchApproval(input, expected, manager)
	require.False(t, ok)

	// Different inner calldata.
	input = encodeExecTransactionHex(t, safe.ExecTransactionInputs{To: manager, Data: []byte{0xaa, 0xcc}, Operation: safe.OperationCall})
	_, ok = matchApproval(input, expected, manager)
	require.False(t, ok)

	// Not hex / not execTransaction calldata.
	_, ok = matchApproval("zz", expected, manager)
	require.False(t, ok)
	_, ok = matchApproval("6a761202deadbeef", expected, manager)
	require.False(t, ok)
}

// insertExecTx inserts an execTransaction-to-safe row as the indexer would
// store it (hex-encoded, no 0x prefix).
func insertExecTx(t *testing.T, db *gorm.DB, safeAddr common.Address, txHash, input string, block, index, status uint64) {
	t.Helper()
	require.NoError(t, db.Create(&database.Transaction{
		Hash:             txHash,
		FunctionSig:      hex.EncodeToString(safe.ExecTransactionSelector[:]),
		Input:            input,
		BlockNumber:      block,
		TransactionIndex: index,
		ToAddress:        hex.EncodeToString(safeAddr[:]),
		Status:           status,
		Timestamp:        block,
	}).Error)
}

// insertApprovedLog inserts a MachinePathListApproved log carrying safeNonce,
// as the indexer would store it (topics + ABI-encoded non-indexed data, hex).
func insertApprovedLog(t *testing.T, db *gorm.DB, manager common.Address, extensionID common.Hash, listNonce uint64, safeAddr common.Address, safeNonce uint32, txHash string, block uint64) {
	t.Helper()
	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)
	data, err := managerABI.Events["MachinePathListApproved"].Inputs.NonIndexed().Pack(safeNonce, [][32]byte{})
	require.NoError(t, err)

	nonceTopic := convert.Uint64ToHash(listNonce)
	safeTopic := common.BytesToHash(safeAddr.Bytes())
	require.NoError(t, db.Create(&database.Log{
		Address:         hex.EncodeToString(manager[:]),
		Topic0:          hex.EncodeToString(machinePathListApprovedEventSel[:]),
		Topic1:          hex.EncodeToString(extensionID[:]),
		Topic2:          hex.EncodeToString(nonceTopic[:]),
		Topic3:          hex.EncodeToString(safeTopic[:]),
		Data:            hex.EncodeToString(data),
		TransactionHash: txHash,
		BlockNumber:     block,
		Timestamp:       block,
	}).Error)
}

// TestCollectSafeApprovalUsesEventNonce exercises the full proxy flow against
// an in-memory indexer: the approval is located via the MachinePathListApproved
// event, the Safe nonce is read straight from that event (no scan, no count),
// and the pinpointed execTransaction supplies the owner signatures.
func TestCollectSafeApprovalUsesEventNonce(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "safe_collect")
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	const (
		chainID    = uint64(14)
		listNonce  = uint64(7)
		approvalAt = uint64(20)
		txIndex    = uint64(2)
		threshold  = uint64(2)
		// An arbitrary Safe nonce unrelated to how many txs precede it — the
		// event is authoritative, so a scan/count would be irrelevant here.
		safeNonce = uint32(42)
	)
	managerAddr := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	safeAddr := common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")

	_, extensionID, _ := sampleHash(t)
	messageHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000abcdef01")
	calldata, err := types.ApproveMachinePathListCalldata(extensionID, listNonce, messageHash)
	require.NoError(t, err)

	owners := ownerAddresses(t, testOwnerKeys)

	tx := safe.Tx{To: managerAddr, Data: calldata, Operation: safe.OperationCall, Nonce: new(big.Int).SetUint64(uint64(safeNonce))}
	txHash, err := tx.Hash(chainID, safeAddr)
	require.NoError(t, err)
	blob := signSafeTx(t, txHash, testOwnerKeys[:2])

	execCalldata := encodeExecTransactionHex(t, safe.ExecTransactionInputs{
		To: managerAddr, Data: calldata, Operation: safe.OperationCall, Signatures: blob,
	})
	approvalTxHash := fmt.Sprintf("%064x", approvalAt*1000+txIndex)
	insertExecTx(t, db, safeAddr, approvalTxHash, execCalldata, approvalAt, txIndex, txSuccessStatus)
	insertApprovedLog(t, db, managerAddr, extensionID, listNonce, safeAddr, safeNonce, approvalTxHash, approvalAt)

	gov := types.Governance{Safe: safeAddr, Signers: owners, Threshold: threshold}
	approval, err := collectSafeApproval(context.Background(), db, gov, managerAddr, extensionID, listNonce, messageHash, chainID, 0, int64(approvalAt))
	require.NoError(t, err)
	require.NotNil(t, approval, "approval should be located and verified")
	require.Equal(t, uint64(safeNonce), approval.Nonce, "nonce read from the event's safeNonce")
	require.Equal(t, calldata, approval.ExecTransaction.Data)
	require.NoError(t, approval.Verify(chainID, safeAddr, owners, threshold))
}

// TestCollectSafeApprovalNoneWhenUnverifiable returns nil (no error) when no
// approval log matches the list.
func TestCollectSafeApprovalNoneWhenUnverifiable(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "safe_collect_none")
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	safeAddr := common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	_, extensionID, _ := sampleHash(t)
	messageHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000abcdef01")

	gov := types.Governance{Safe: safeAddr, Signers: []common.Address{{1}, {2}}, Threshold: 2}
	approval, err := collectSafeApproval(context.Background(), db, gov, manager, extensionID, 7, messageHash, 14, 0, 0)
	require.NoError(t, err)
	require.Nil(t, approval)
}
