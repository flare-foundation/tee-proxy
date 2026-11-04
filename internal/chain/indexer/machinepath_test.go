package indexer

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
)

const testChainID = uint64(14)

var (
	testManager     = common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	testSafe        = common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")
	testExtensionID = common.HexToHash("0x2a")
	testPaths       = []cmpaths.IMachinePathManagerMachinePath{
		testutil.MachinePath(common.HexToAddress("0x1111111111111111111111111111111111111111"), common.HexToAddress("0x2222222222222222222222222222222222222222")),
	}
)

func newDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, _ := testutil.InMemoryDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}, &database.State{}, &database.Block{}))

	return db
}

func newSource(db *gorm.DB) *Source {
	return New(db, config.Addresses{MachinePathManager: testManager}, database.SyncParams{})
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	k, err := crypto.GenerateKey()
	require.NoError(t, err)

	return k
}

// TestRecoverSignMachinePathListInputs verifies that calldata produced by the
// generated binding round-trips through the decoder.
func TestRecoverSignMachinePathListInputs(t *testing.T) {
	const nonce = uint64(7)
	hash := testutil.MachinePathListHash(t, testChainID, testExtensionID, nonce, testPaths)
	sig := testutil.GovernanceSignature(t, hash, newKey(t))

	input := testutil.SignMachinePathListInput(t, testExtensionID, nonce, sig)

	gotExt, gotNonce, gotSig, err := recoverSignMachinePathListInputs(hex.EncodeToString(input))
	require.NoError(t, err)
	assert.Equal(t, 0, gotExt.Cmp(new(big.Int).SetBytes(testExtensionID[:])))
	assert.Equal(t, 0, gotNonce.Cmp(new(big.Int).SetUint64(nonce)))
	assert.Equal(t, machinepathmanager.Signature{V: sig.V, R: sig.R, S: sig.S}, gotSig)
}

func TestRecoverSignMachinePathListInputsInvalid(t *testing.T) {
	_, _, _, err := recoverSignMachinePathListInputs("zzzz")
	assert.Error(t, err)

	_, _, _, err = recoverSignMachinePathListInputs("00")
	assert.Error(t, err)
}

// TestSetMachinePathListActionNoAuthorization guards the joint-insufficiency branch: an
// activated list with no signMachinePathList transaction and no Safe-backed governance
// must fail with ErrNoAuthorization so callers can classify it apart from infra faults.
func TestSetMachinePathListActionNoAuthorization(t *testing.T) {
	db := newDB(t)
	const nonce = uint64(7)

	testutil.InsertPathsAdded(t, db, testManager, testExtensionID, nonce, testPaths, 100, 0, 100)
	testutil.InsertListSigned(t, db, testManager, testExtensionID, nonce, 110)

	_, err := machinepath.SetMachinePathListAction(context.Background(), newSource(db), testManager, types.Governance{}, testExtensionID, testChainID, nonce)
	require.ErrorIs(t, err, machinepath.ErrNoAuthorization)
}

// insertSafeApproval indexes a Safe execTransaction approving the list with
// two of three owners at safeNonce, plus the MachinePathListApproved log it emitted.
func insertSafeApproval(t *testing.T, db *gorm.DB, nonce uint64, safeNonce uint32, block uint64) []byte {
	t.Helper()

	messageHash := testutil.MachinePathListHash(t, testChainID, testExtensionID, nonce, testPaths)
	calldata, err := types.ApproveMachinePathListCalldata(testExtensionID, nonce, messageHash)
	require.NoError(t, err)

	tx := safe.Tx{To: testManager, Data: calldata, Operation: safe.OperationCall, Nonce: new(big.Int).SetUint64(uint64(safeNonce))}
	txHash, err := tx.Hash(testChainID, testSafe)
	require.NoError(t, err)
	execCalldata, err := safe.EncodeExecTransactionCalldata(safe.ExecTransactionInputs{
		To: testManager, Data: calldata, Operation: safe.OperationCall,
		Signatures: testutil.SignSafeTx(t, txHash, testutil.SafeOwnerKeys[:2]),
	})
	require.NoError(t, err)

	approvalTx := common.BigToHash(new(big.Int).SetUint64(block*1000 + 2))
	testutil.InsertTx(t, db, testutil.TxRow{To: testSafe, Input: execCalldata, Block: block, Index: 2, Timestamp: block, Status: txSuccessStatus, Hash: approvalTx})

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)
	testutil.InsertLog(t, db, testutil.LogRow{
		Address:   testManager,
		Event:     managerABI.Events["MachinePathListApproved"],
		Topics:    []common.Hash{testExtensionID, common.BigToHash(new(big.Int).SetUint64(nonce)), common.BytesToHash(testSafe[:])},
		Args:      []any{safeNonce, [][32]byte{}},
		Block:     block,
		Timestamp: block,
		TxHash:    approvalTx,
	})

	return calldata
}

// TestSetMachinePathListActionPureSafe locks down the Safe-only attach path: with no
// direct signatures and a verifiable Safe approval on chain, the built action's request
// must carry the approval and an empty signature set.
func TestSetMachinePathListActionPureSafe(t *testing.T) {
	db := newDB(t)
	const (
		nonce     = uint64(7)
		threshold = uint64(2)
	)

	testutil.InsertPathsAdded(t, db, testManager, testExtensionID, nonce, testPaths, 100, 0, 100)
	insertSafeApproval(t, db, nonce, 42, 105)
	testutil.InsertListSigned(t, db, testManager, testExtensionID, nonce, 110)

	owners := testutil.OwnerAddresses(t, testutil.SafeOwnerKeys)
	gov := types.Governance{Safe: testSafe, Signers: owners, Threshold: threshold}
	action, err := machinepath.SetMachinePathListAction(context.Background(), newSource(db), testManager, gov, testExtensionID, testChainID, nonce)
	require.NoError(t, err)

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	var req types.SetMachinePathListRequest
	require.NoError(t, json.Unmarshal(di.Message, &req))

	require.NotNil(t, req.SafeApproval, "the Safe approval must be attached")
	require.Empty(t, req.Signatures, "no direct signatures exist for the list")
	require.NoError(t, req.SafeApproval.Verify(testChainID, testSafe, owners, threshold))
}

// TestSafeApprovalsUseEventNonce locates the approval via the MachinePathListApproved
// event at the window's inclusive upper bound, reads the Safe nonce straight from that
// event (no scan, no count), and takes the owner signatures from the pinpointed
// execTransaction.
func TestSafeApprovalsUseEventNonce(t *testing.T) {
	db := newDB(t)
	const (
		nonce      = uint64(7)
		approvalAt = uint64(20)
		// arbitrary: the event is authoritative, unrelated to preceding txs
		safeNonce = uint32(42)
	)

	calldata := insertSafeApproval(t, db, nonce, safeNonce, approvalAt)

	approvals, err := newSource(db).safeApprovals(context.Background(), testExtensionID, nonce, testSafe, 0, int64(approvalAt))
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	require.Equal(t, uint64(safeNonce), approvals[0].Nonce, "nonce read from the event's safeNonce")
	require.Equal(t, calldata, approvals[0].ExecTransaction.Data)
	require.NoError(t, approvals[0].Verify(testChainID, testSafe, testutil.OwnerAddresses(t, testutil.SafeOwnerKeys), 2))
}

// TestSafeApprovalsOnlyForList keeps only the requested list's approvals by its Safe,
// though the window holds approvals of other lists.
func TestSafeApprovalsOnlyForList(t *testing.T) {
	db := newDB(t)
	insertSafeApproval(t, db, 7, 1, 20)
	insertSafeApproval(t, db, 8, 2, 21)
	src := newSource(db)

	tests := []struct {
		name       string
		extension  common.Hash
		safe       common.Address
		wantNonces []uint64
	}{
		{name: "list", extension: testExtensionID, safe: testSafe, wantNonces: []uint64{1}},
		{name: "other extension", extension: common.HexToHash("0x2b"), safe: testSafe},
		{name: "other safe", extension: testExtensionID, safe: common.HexToAddress("0x5afe")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approvals, err := src.safeApprovals(context.Background(), tc.extension, 7, tc.safe, 0, 100)
			require.NoError(t, err)

			var nonces []uint64
			for _, a := range approvals {
				nonces = append(nonces, a.Nonce)
			}
			require.Equal(t, tc.wantNonces, nonces)
		})
	}
}

// TestSafeApprovalsSkipsUnusable returns no candidate when no approval log is
// indexed, or when the emitting transaction is not decodable execTransaction calldata.
func TestSafeApprovalsSkipsUnusable(t *testing.T) {
	db := newDB(t)
	src := newSource(db)

	approvals, err := src.safeApprovals(context.Background(), testExtensionID, 7, testSafe, 0, 100)
	require.NoError(t, err)
	require.Empty(t, approvals)

	insertSafeApproval(t, db, 7, 1, 20)
	require.NoError(t, db.Model(&database.Transaction{}).Where("block_number = ?", 20).Update("input", "6a761202deadbeef").Error)

	approvals, err = src.safeApprovals(context.Background(), testExtensionID, 7, testSafe, 0, 100)
	require.NoError(t, err)
	require.Empty(t, approvals)
}
