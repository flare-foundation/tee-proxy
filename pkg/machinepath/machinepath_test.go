package machinepath

import (
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

func sampleHash(t *testing.T) (hash, extensionID common.Hash) {
	t.Helper()

	chainID := uint64(14)
	extensionID = common.HexToHash("0x000000000000000000000000000000000000000000000000000000000000002a")
	nonce := uint64(7)
	paths := []cmpaths.IMachinePathManagerMachinePath{{
		SourceTeeIds:      []common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111")},
		DestinationTeeIds: []common.Address{common.HexToAddress("0x2222222222222222222222222222222222222222")},
	}}

	dataHash, err := types.MachinePathListDataHash(extensionID, nonce, paths)
	require.NoError(t, err)
	signHash, err := csigning.NewPayload(csigning.TEEMachinePathList, chainID, dataHash).Hash()
	require.NoError(t, err)
	hash = common.BytesToHash(signHash[:])

	return hash, extensionID
}

// TestSerializeAndRecover verifies that a serialized governance signature is
// recovered to the original signer by the TEE node's recovery routine, proving
// the [R||S||V-27] format and EIP-191 prefixing match what the node verifies.
func TestSerializeAndRecover(t *testing.T) {
	hash, _ := sampleHash(t)

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

func TestCollectSignatures(t *testing.T) {
	hash, _ := sampleHash(t)

	const (
		ownKey     = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"
		foreignKey = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	own := signAsGovernance(t, hash, ownKey)
	foreign := signAsGovernance(t, hash, foreignKey)
	broken := machinepathmanager.Signature{V: 27}

	priv, err := crypto.HexToECDSA(ownKey)
	require.NoError(t, err)
	governance := []common.Address{crypto.PubkeyToAddress(priv.PublicKey)}

	tests := []struct {
		name    string
		sigs    []machinepathmanager.Signature
		signers []common.Address
		want    [][]byte
	}{
		{
			name: "duplicate and unrecoverable dropped",
			sigs: []machinepathmanager.Signature{own, broken, foreign, own},
			want: [][]byte{serializeSig(own), serializeSig(foreign)},
		},
		{
			name:    "foreign signer dropped",
			sigs:    []machinepathmanager.Signature{foreign, own},
			signers: governance,
			want:    [][]byte{serializeSig(own)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, collectSignatures(tc.sigs, hash, tc.signers))
		})
	}
}
