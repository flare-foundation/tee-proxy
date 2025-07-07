package policy

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/stretchr/testify/require"
)

func TestRecoverInputs(t *testing.T) {
	// from flare tx 0x15b909bd6caa08d3b9ea48aa1e9a9251429891282d1badf5f6be0dfefaac0f83

	inputHex := "8f7d09570000000000000000000000001645a43ec5d09a0f0110683b5f5a4dc2ffcef17d000000000000000000000000000000000000000000000000000000000000001c49dcc77d07202cb33804b100ff8712b7b4b9bf4e413bd5b416de59c97bad237d53e69ee7d06354d0503cce7e491abf2e26dcb5456603deb208e74c5b2e68b96a"

	input, err := hex.DecodeString(inputHex)
	require.NoError(t, err)

	address, sig, err := recoverInputs(input)
	require.NoError(t, err)

	expectedAddress := common.HexToAddress("0x1645A43eC5d09a0F0110683B5F5a4Dc2FFCef17d")
	require.Equal(t, expectedAddress, address)

	expectedSig := &registry.IVoterRegistrySignature{
		V: 28,
		R: common.HexToHash("0x49dcc77d07202cb33804b100ff8712b7b4b9bf4e413bd5b416de59c97bad237d"),
		S: common.HexToHash("0x53e69ee7d06354d0503cce7e491abf2e26dcb5456603deb208e74c5b2e68b96a"),
	}

	require.Equal(t, expectedSig, sig)

	pub, err := recoverPubKeyFromRegistration(address, 309, sig)
	require.NoError(t, err)

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	expectedRecoveredAddress := common.HexToAddress("0xDefaF698d59fE7BbB58950092e56dA492079FB75")
	require.Equal(t, expectedRecoveredAddress, recoveredAddress)
}
