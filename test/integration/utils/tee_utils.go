package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
)

func SetProxyURLOnTEE(t *testing.T, port uint, proxyURL string) {
	t.Helper()

	request := types.ConfigureProxyURLRequest{
		URL: &proxyURL,
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/proxy", port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	err = resp.Body.Close()
	require.NoError(t, err)
}

// SetChainIDOnTEE configures the TEE node's chain ID. The node refuses to process
// instructions until this is set, and the value must match the chain ID the actions
// hash their messages with.
func SetChainIDOnTEE(t *testing.T, port uint, chainID uint64) {
	t.Helper()

	request := types.ConfigureChainIDRequest{
		ChainID: &chainID,
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/chain-id", port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	err = resp.Body.Close()
	require.NoError(t, err)
}
