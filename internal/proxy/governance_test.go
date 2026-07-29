package proxy

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/stretchr/testify/require"
)

var (
	testSigners = []common.Address{
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		common.HexToAddress("0x2222222222222222222222222222222222222222"),
		common.HexToAddress("0x3333333333333333333333333333333333333333"),
	}
	testSafe       = common.HexToAddress("0x5afe5afe5afe5afe5afe5afe5afe5afe5afe5afe")
	testTeeManager = common.HexToAddress("0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE")
)

// TestResolveGovernanceUnsetIsLegacyPath: no governance config returns the zero
// value (Safe pre-verification disabled) and never consults the node hash.
func TestResolveGovernanceUnsetIsLegacyPath(t *testing.T) {
	gov, err := resolveGovernance(config.Governance{}, common.HexToHash("0xdeadbeef"))
	require.NoError(t, err)
	require.Equal(t, types.Governance{}, gov)
	require.Equal(t, common.Address{}, gov.Safe) // machinepath service treats this as "no Safe pre-verify"
}

func TestResolveGovernancePlainMatch(t *testing.T) {
	const threshold = uint64(2)
	nodeHash, err := types.GovernanceHash(testSigners, threshold)
	require.NoError(t, err)

	gov, err := resolveGovernance(config.Governance{Signers: testSigners, Threshold: threshold}, nodeHash)
	require.NoError(t, err)
	require.Equal(t, testSigners, gov.Signers)
	require.Equal(t, threshold, gov.Threshold)
	require.Equal(t, common.Address{}, gov.Safe)
	require.Equal(t, nodeHash, gov.Hash)
}

func TestResolveGovernanceSafeMatch(t *testing.T) {
	const threshold = uint64(2)
	nodeHash, err := types.GovernanceHashSafe(testTeeManager, testSafe, testSigners, threshold)
	require.NoError(t, err)

	gov, err := resolveGovernance(config.Governance{
		Signers: testSigners, Threshold: threshold, Safe: testSafe, TeeManager: testTeeManager,
	}, nodeHash)
	require.NoError(t, err)
	require.Equal(t, testSafe, gov.Safe)
	require.Equal(t, testTeeManager, gov.TeeManager)
	require.Equal(t, nodeHash, gov.Hash)
}

func TestResolveGovernanceMismatchRejected(t *testing.T) {
	const threshold = uint64(2)
	// The node is bound to the plain-flavor hash, but the proxy is configured
	// with a Safe flavor (different preimage) — must be rejected.
	nodeHash, err := types.GovernanceHash(testSigners, threshold)
	require.NoError(t, err)

	_, err = resolveGovernance(config.Governance{
		Signers: testSigners, Threshold: threshold, Safe: testSafe, TeeManager: testTeeManager,
	}, nodeHash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match the node's governance hash")

	// A different signer set also mismatches.
	_, err = resolveGovernance(config.Governance{
		Signers: testSigners[:2], Threshold: threshold,
	}, nodeHash)
	require.Error(t, err)
}
