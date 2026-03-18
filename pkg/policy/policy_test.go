package policy

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/stretchr/testify/require"
)

// TestRecoverInputsRegisterVoterLegacy verifies the legacy path (chain_id not set)
// using a real Flare mainnet transaction. This path is used only for Coston which
// still runs the old VoterRegistry contract without chainId in the message hash.
// TODO: Remove this test once Coston is upgraded to the new VoterRegistry contract.
func TestRecoverInputsRegisterVoterLegacy(t *testing.T) {
	// from flare tx 0x15b909bd6caa08d3b9ea48aa1e9a9251429891282d1badf5f6be0dfefaac0f83
	sig := &registry.Signature{
		V: 28,
		R: common.HexToHash("0x49dcc77d07202cb33804b100ff8712b7b4b9bf4e413bd5b416de59c97bad237d"),
		S: common.HexToHash("0x53e69ee7d06354d0503cce7e491abf2e26dcb5456603deb208e74c5b2e68b96a"),
	}

	idAddress := common.HexToAddress("0x1645a43ec5d09a0f0110683b5f5a4dc2ffcef17d")

	// chainID nil triggers the legacy path: messageHash = keccak256(abi.encode(rewardEpochId, voter))
	pub, err := recoverPubKeyFromRegistration(idAddress, 309, sig, nil)
	require.NoError(t, err)

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	expectedRecoveredAddress := common.HexToAddress("0xDefaF698d59fE7BbB58950092e56dA492079FB75")
	require.Equal(t, expectedRecoveredAddress, recoveredAddress)
}

// TestRecoverInputsRegisterVoterWithChainId verifies the new path (chain_id set)
// where messageHash = keccak256(abi.encode(block.chainid, rewardEpochId, voter)).
func TestRecoverInputsRegisterVoterWithChainId(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	expectedAddress := crypto.PubkeyToAddress(privKey.PublicKey)

	chainID := big.NewInt(31337)
	rewardEpochID := uint32(1)

	// Construct the message the same way as the new contract
	msg, err := msgArgs.Pack(chainID, rewardEpochID, expectedAddress)
	require.NoError(t, err)

	sigMsg := accounts.TextHash(crypto.Keccak256(msg))
	sigBytes, err := crypto.Sign(sigMsg, privKey)
	require.NoError(t, err)

	sig := &registry.Signature{
		V: sigBytes[64] + 27,
		R: common.BytesToHash(sigBytes[0:32]),
		S: common.BytesToHash(sigBytes[32:64]),
	}

	pub, err := recoverPubKeyFromRegistration(expectedAddress, rewardEpochID, sig, chainID)
	require.NoError(t, err)

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	require.Equal(t, expectedAddress, recoveredAddress)
}

func TestRecoverInputsSignNewSigningPolicy(t *testing.T) {
	// from flare tx 0x8c8a7dc6341acf9003fbf9f298614654e06abd668e25b4a8eba069ef13c6d635

	inputHex := "6b4c7bd6000000000000000000000000000000000000000000000000000000000000013596986bfe5ffa787b6d33f4b16b3d40d5a46f38972c271771ddbe47fb142a0363000000000000000000000000000000000000000000000000000000000000001b0f5b007631ecd9b02258eb321f51b2be0728660c528ce47454de7c141e69259613d839242eb3ebe673a04a041ef105c5c3eb4d9a4c3943d98de5966ac3bc0392"

	input, err := hex.DecodeString(inputHex)
	require.NoError(t, err)

	spID, hash, sig, err := recoverInputsSignNewSigningPolicy(input)
	require.NoError(t, err)

	require.Equal(t, uint32(309), spID)

	expectedHash := common.HexToHash("0x96986bfe5ffa787b6d33f4b16b3d40d5a46f38972c271771ddbe47fb142a0363")
	require.Equal(t, expectedHash, hash)

	expectedSig := &registry.Signature{
		V: 27,
		R: common.HexToHash("0x0f5b007631ecd9b02258eb321f51b2be0728660c528ce47454de7c141e692596"),
		S: common.HexToHash("0x13d839242eb3ebe673a04a041ef105c5c3eb4d9a4c3943d98de5966ac3bc0392"),
	}
	require.Equal(t, expectedSig, sig)

	pub, err := recoverSigner(hash, sig)
	require.NoError(t, err)

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	expectedRecoveredAddress := common.HexToAddress("0xdA3DbEFdc13E86DCD5443F514aa11c1F63Db3Cbf")
	require.Equal(t, expectedRecoveredAddress, recoveredAddress)
}
