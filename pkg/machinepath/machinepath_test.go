package machinepath

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
