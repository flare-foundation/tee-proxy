package machinepath_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/chain/indexer"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
)

func TestSetMachinePathListActionPathOrderAndStatus(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	const (
		chainID     = uint64(14)
		nonce       = uint64(4)
		signedBlock = uint64(110)
	)
	manager := common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
	extensionID := common.HexToHash("0x2a")

	p0 := testutil.MachinePath(common.HexToAddress("0xa0"), common.HexToAddress("0xb0"))
	p1 := testutil.MachinePath(common.HexToAddress("0xa1"), common.HexToAddress("0xb1"))
	p2 := testutil.MachinePath(common.HexToAddress("0xa2"), common.HexToAddress("0xb2"))
	want := []cmpaths.IMachinePathManagerMachinePath{p0, p1, p2}

	// The indexer returns newest first; on-chain order is (block, log index).
	testutil.InsertPathsAdded(t, db, manager, extensionID, nonce, want[2:3], 100, 1, 100)
	testutil.InsertPathsAdded(t, db, manager, extensionID, nonce, want[1:2], 100, 0, 100)
	testutil.InsertPathsAdded(t, db, manager, extensionID, nonce, want[0:1], 98, 5, 98)
	testutil.InsertListSigned(t, db, manager, extensionID, nonce, signedBlock)

	hash := testutil.MachinePathListHash(t, chainID, extensionID, nonce, want)
	signer, reverted := newKey(t), newKey(t)
	signerSig := testutil.GovernanceSignature(t, hash, signer)
	testutil.InsertTx(t, db, testutil.TxRow{To: manager, Input: testutil.SignMachinePathListInput(t, extensionID, nonce, signerSig), Block: 99, Timestamp: 99, Status: 1})
	testutil.InsertTx(t, db, testutil.TxRow{To: manager, Input: testutil.SignMachinePathListInput(t, extensionID, nonce, testutil.GovernanceSignature(t, hash, reverted)), Block: 101, Timestamp: 101, Status: 0})

	src := indexer.New(db, config.Addresses{MachinePathManager: manager}, database.SyncParams{})
	action, err := machinepath.SetMachinePathListAction(context.Background(), src, manager, types.Governance{}, extensionID, chainID, nonce)
	require.NoError(t, err)

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	var req types.SetMachinePathListRequest
	require.NoError(t, json.Unmarshal(di.Message, &req))

	require.Equal(t, want, req.Paths)
	require.Equal(t, nonce, req.Nonce)
	require.Equal(t, [][]byte{testutil.SerializedSignature(signerSig)}, req.Signatures)
	require.Nil(t, req.SafeApproval)
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	return k
}
