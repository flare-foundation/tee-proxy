package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MultisigResult represents the result of creating a multisig wallet
type MultisigResult struct {
	Success         bool   `json:"success"`
	MultisigAddress string `json:"multisigAddress"`
	Balance         int    `json:"balance"`
	Error           string `json:"error,omitempty"`
}

// CreateMultisigWallet calls the TypeScript CLI to create a multisig wallet
func CreateMultisigWallet(t *testing.T, walletAddresses []string, quorum int) *MultisigResult {
	t.Helper()

	// Get the path to the scripts directory
	workDir, err := filepath.Abs("./scripts")
	require.NoError(t, err, "Failed to get scripts directory path")

	// Check if scripts directory exists
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		t.Fatalf("Scripts directory does not exist: %s", workDir)
	}

	// Prepare command arguments
	args := []string{"run", "cli", fmt.Sprintf("%d", quorum)}
	args = append(args, walletAddresses...)

	// Create the command
	cmd := exec.Command("npm", args...)
	cmd.Dir = workDir

	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Command failed: %v", err)
		t.Logf("Output: %s", string(output))
		t.Fatalf("Failed to execute CLI command: %v", err)
	}

	// Parse the JSON output
	var result MultisigResult
	outputStr := strings.TrimSpace(string(output))

	// Handle case where there might be extra output before/after JSON
	lines := strings.Split(outputStr, "\n")
	var jsonLine string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			jsonLine = line
			break
		}
	}

	if jsonLine == "" {
		t.Fatalf("No valid JSON output found. Output: %s", outputStr)
	}

	err = json.Unmarshal([]byte(jsonLine), &result)
	require.NoError(t, err, "Failed to parse CLI output: %s", jsonLine)

	// Check if the operation was successful
	if !result.Success {
		t.Fatalf("CLI operation failed: %s", result.Error)
	}

	t.Logf("Created multisig wallet: %s with balance: %d", result.MultisigAddress, result.Balance)
	return &result
}
