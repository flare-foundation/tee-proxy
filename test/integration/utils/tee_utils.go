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

func SetProxyUrlOnTee(t *testing.T, port uint, proxyUrl string) {
	t.Helper()

	request := types.ConfigureProxyUrlRequest{
		Url: proxyUrl,
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/configure", port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
