package testutil

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// MachinePath returns a one-source, one-destination path.
func MachinePath(source, destination common.Address) cmpaths.IMachinePathManagerMachinePath {
	return cmpaths.IMachinePathManagerMachinePath{
		SourceTeeIds:      []common.Address{source},
		DestinationTeeIds: []common.Address{destination},
	}
}

// MachinePathListHash is the message governance signs for the list.
func MachinePathListHash(t *testing.T, chainID uint64, extensionID common.Hash, nonce uint64, paths []cmpaths.IMachinePathManagerMachinePath) common.Hash {
	t.Helper()

	dataHash, err := types.MachinePathListDataHash(extensionID, nonce, paths)
	require.NoError(t, err)
	hash, err := csigning.NewPayload(csigning.TEEMachinePathList, chainID, dataHash).Hash()
	require.NoError(t, err)

	return common.BytesToHash(hash[:])
}

// InsertPathsAdded stores a MachinePathsAdded log for (extensionID, nonce).
func InsertPathsAdded(t *testing.T, db *gorm.DB, manager common.Address, extensionID common.Hash, nonce uint64, paths []cmpaths.IMachinePathManagerMachinePath, block, index, timestamp uint64) {
	t.Helper()

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)

	out := make([]machinepathmanager.IMachinePathManagerMachinePath, len(paths))
	for i, p := range paths {
		out[i] = machinepathmanager.IMachinePathManagerMachinePath{SourceTeeIds: p.SourceTeeIds, DestinationTeeIds: p.DestinationTeeIds}
	}

	InsertLog(t, db, LogRow{
		Address:   manager,
		Event:     managerABI.Events["MachinePathsAdded"],
		Topics:    []common.Hash{extensionID, convert.Uint64ToHash(nonce)},
		Args:      []any{out},
		Block:     block,
		Timestamp: timestamp,
		Index:     index,
	})
}

// InsertListSigned stores the MachinePathListSigned log activating (extensionID, nonce).
func InsertListSigned(t *testing.T, db *gorm.DB, manager common.Address, extensionID common.Hash, nonce, block uint64) {
	t.Helper()

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)

	InsertLog(t, db, LogRow{
		Address:   manager,
		Event:     managerABI.Events["MachinePathListSigned"],
		Topics:    []common.Hash{extensionID, convert.Uint64ToHash(nonce)},
		Block:     block,
		Timestamp: block,
	})
}

// GovernanceSignature is key's EIP-191 signature over hash as signMachinePathList carries it.
func GovernanceSignature(t *testing.T, hash common.Hash, key *ecdsa.PrivateKey) registry.Signature {
	t.Helper()
	return sign(t, accounts.TextHash(hash[:]), key)
}

// SignMachinePathListInput returns signMachinePathList calldata carrying sig.
func SignMachinePathListInput(t *testing.T, extensionID common.Hash, nonce uint64, sig registry.Signature) []byte {
	t.Helper()

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)
	method := managerABI.Methods["signMachinePathList"]

	packed, err := method.Inputs.Pack(
		new(big.Int).SetBytes(extensionID[:]),
		new(big.Int).SetUint64(nonce),
		machinepathmanager.Signature{V: sig.V, R: sig.R, S: sig.S},
	)
	require.NoError(t, err)

	return append(append([]byte{}, method.ID...), packed...)
}
