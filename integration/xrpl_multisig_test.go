package integration

import (
	"testing"

	integrationUtils "github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/stretchr/testify/require"
)

func TestCreateMultisigWallet(t *testing.T) {
	t.Skip("Skipping multisig wallet creation")

	walletAddresses := []string{
		"rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G", // Example test address 1
		"rJQesZZEQzW9J3Eb1X1Snc7E6YGk7kTMoK", // Example test address 2
		"r9cvJhquqeExszdWZSw2rrFP98fsVFLdPe", // Example test address 3
	}

	quorum := 2 // Require 2 out of 3 signatures

	// Create the multisig wallet
	result := integrationUtils.CreateMultisigWallet(t, walletAddresses, quorum)

	// Verify the result
	require.NotEmpty(t, result.MultisigAddress, "Multisig address should not be empty")
	require.Greater(t, result.Balance, 0, "Balance should be greater than 0")

	t.Logf("Successfully created multisig wallet:")
	t.Logf("  Address: %s", result.MultisigAddress)
	t.Logf("  Balance: %d", result.Balance)
}
