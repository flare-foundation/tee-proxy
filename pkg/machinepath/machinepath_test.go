package machinepath

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// signAsGovernance reproduces an on-chain governance signature: it signs the
// EIP-191 prefixed message hash and returns the contract Signature struct with
// the Ethereum-style v in {27, 28}.
func signAsGovernance(t *testing.T, hash common.Hash, key string) machinepathmanager.Signature {
	t.Helper()

	priv, err := crypto.HexToECDSA(key)
	require.NoError(t, err)

	raw, err := crypto.Sign(accounts.TextHash(hash[:]), priv)
	require.NoError(t, err)

	var sig machinepathmanager.Signature
	copy(sig.R[:], raw[:32])
	copy(sig.S[:], raw[32:64])
	sig.V = raw[64] + 27

	return sig
}

func sampleHash(t *testing.T) (hash, extensionID common.Hash, nonce uint64) {
	t.Helper()

	chainID := uint64(14)
	extensionID = common.HexToHash("0x000000000000000000000000000000000000000000000000000000000000002a")
	nonce = uint64(7)
	paths := []cmpaths.IMachinePathManagerMachinePath{{
		SourceTeeIds:      []common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111")},
		DestinationTeeIds: []common.Address{common.HexToAddress("0x2222222222222222222222222222222222222222")},
	}}

	dataHash, err := types.MachinePathListDataHash(extensionID, nonce, paths)
	require.NoError(t, err)
	signHash, err := csigning.NewPayload(csigning.TEEMachinePathList, chainID, dataHash).Hash()
	require.NoError(t, err)
	hash = common.BytesToHash(signHash[:])

	return hash, extensionID, nonce
}

// TestSerializeAndRecover verifies that a serialized governance signature is
// recovered to the original signer by the TEE node's recovery routine, proving
// the [R||S||V-27] format and EIP-191 prefixing match what the node verifies.
func TestSerializeAndRecover(t *testing.T) {
	hash, _, _ := sampleHash(t)

	const key = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"
	priv, err := crypto.HexToECDSA(key)
	require.NoError(t, err)
	want := crypto.PubkeyToAddress(priv.PublicKey)

	sig := signAsGovernance(t, hash, key)
	serialized := serializeSig(sig)
	require.Len(t, serialized, 65)
	require.LessOrEqual(t, serialized[64], byte(1)) // v normalized to {0,1}

	// collectSignatures recovers signers with the same node routine; it must
	// recover the original signer from these bytes.
	got, err := teeutils.SignatureToSignersAddress(hash[:], serialized)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestRecoverSignMachinePathListInputs verifies that calldata produced by the
// generated binding round-trips through the proxy's decoder.
func TestRecoverSignMachinePathListInputs(t *testing.T) {
	hash, extensionID, nonce := sampleHash(t)
	sig := signAsGovernance(t, hash, "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")

	extIDBig := new(big.Int).SetBytes(extensionID[:])
	nonceBig := new(big.Int).SetUint64(nonce)

	packed, err := signMachinePathListArgs.Pack(extIDBig, nonceBig, sig)
	require.NoError(t, err)

	input := append(append([]byte{}, signMachinePathListSel[:]...), packed...)

	gotExt, gotNonce, gotSig, err := recoverSignMachinePathListInputs(hex.EncodeToString(input))
	require.NoError(t, err)
	assert.Equal(t, 0, gotExt.Cmp(extIDBig))
	assert.Equal(t, 0, gotNonce.Cmp(nonceBig))
	assert.Equal(t, sig, gotSig)
}

func TestRecoverSignMachinePathListInputsInvalid(t *testing.T) {
	_, _, _, err := recoverSignMachinePathListInputs("zzzz")
	assert.Error(t, err)

	_, _, _, err = recoverSignMachinePathListInputs("00")
	assert.Error(t, err)
}

// insertAddedLog inserts the MachinePathsAdded log for the sampleHash list — the
// same paths sampleHash hashes — so SetMachinePathListAction reconstructs exactly
// the message hash sampleHash returns.
func insertAddedLog(t *testing.T, db *gorm.DB, manager common.Address, extensionID common.Hash, nonce, block uint64) {
	t.Helper()

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)
	addedEvent, ok := managerABI.Events["MachinePathsAdded"]
	require.True(t, ok)

	paths := []machinepathmanager.IMachinePathManagerMachinePath{{
		SourceTeeIds:      []common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111")},
		DestinationTeeIds: []common.Address{common.HexToAddress("0x2222222222222222222222222222222222222222")},
	}}
	data, err := addedEvent.Inputs.NonIndexed().Pack(paths)
	require.NoError(t, err)

	nonceTopic := convert.Uint64ToHash(nonce)
	require.NoError(t, db.Create(&database.Log{
		Address:         hex.EncodeToString(manager[:]),
		Data:            hex.EncodeToString(data),
		Topic0:          hex.EncodeToString(addedEvent.ID[:]),
		Topic1:          hex.EncodeToString(extensionID[:]),
		Topic2:          hex.EncodeToString(nonceTopic[:]),
		TransactionHash: "bbbb000000000000000000000000000000000000000000000000000000000001",
		LogIndex:        0,
		Timestamp:       block,
		BlockNumber:     block,
	}).Error)
}

// TestSetMachinePathListActionNoAuthorization guards the joint-insufficiency branch: an
// activated list with no signMachinePathList transaction and no Safe-backed governance
// must fail with ErrNoAuthorization so callers can classify it apart from infra faults.
func TestSetMachinePathListActionNoAuthorization(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "machinepath_no_auth")
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	const (
		chainID    = uint64(14)
		addedBlock = uint64(100)
		toBlock    = uint64(110)
	)
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	_, extensionID, nonce := sampleHash(t)

	insertAddedLog(t, db, manager, extensionID, nonce, addedBlock)

	_, err := SetMachinePathListAction(context.Background(), db, manager, types.Governance{}, extensionID, chainID, nonce, toBlock)
	require.ErrorIs(t, err, ErrNoAuthorization)
}

// TestSetMachinePathListActionPureSafe locks down the Safe-only attach path: with no
// direct signatures and a verifiable Safe approval on chain, the built action's request
// must carry the approval and an empty signature set.
func TestSetMachinePathListActionPureSafe(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "machinepath_pure_safe")
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	const (
		chainID    = uint64(14)
		threshold  = uint64(2)
		safeNonce  = uint32(42)
		addedBlock = uint64(100)
		approvalAt = uint64(105)
		toBlock    = uint64(110)
	)
	managerAddr := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	safeAddr := common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")

	messageHash, extensionID, nonce := sampleHash(t)
	insertAddedLog(t, db, managerAddr, extensionID, nonce, addedBlock)

	calldata, err := types.ApproveMachinePathListCalldata(extensionID, nonce, messageHash)
	require.NoError(t, err)

	owners := ownerAddresses(t, testOwnerKeys)
	tx := safe.Tx{To: managerAddr, Data: calldata, Operation: safe.OperationCall, Nonce: new(big.Int).SetUint64(uint64(safeNonce))}
	txHash, err := tx.Hash(chainID, safeAddr)
	require.NoError(t, err)
	blob := signSafeTx(t, txHash, testOwnerKeys[:2])
	execCalldata := encodeExecTransactionHex(t, safe.ExecTransactionInputs{
		To: managerAddr, Data: calldata, Operation: safe.OperationCall, Signatures: blob,
	})
	approvalTxHash := fmt.Sprintf("%064x", approvalAt)
	insertExecTx(t, db, safeAddr, approvalTxHash, execCalldata, approvalAt, 0, txSuccessStatus)
	insertApprovedLog(t, db, managerAddr, extensionID, nonce, safeAddr, safeNonce, approvalTxHash, approvalAt)

	gov := types.Governance{Safe: safeAddr, Signers: owners, Threshold: threshold}
	action, err := SetMachinePathListAction(context.Background(), db, managerAddr, gov, extensionID, chainID, nonce, toBlock)
	require.NoError(t, err)

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	var req types.SetMachinePathListRequest
	require.NoError(t, json.Unmarshal(di.Message, &req))

	require.NotNil(t, req.SafeApproval, "the Safe approval must be attached")
	require.Empty(t, req.Signatures, "no direct signatures exist for the list")
	require.NoError(t, req.SafeApproval.Verify(chainID, safeAddr, owners, threshold))
}
