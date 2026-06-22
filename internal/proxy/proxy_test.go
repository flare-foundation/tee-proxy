package proxy

import (
	"testing"

	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/stretchr/testify/require"
)

// TestDirectConfigCopiesAllFields ensures every /direct configuration field, in particular
// the body-size limit, reaches the server's DirectConfig.
func TestDirectConfigCopiesAllFields(t *testing.T) {
	c := config.Direct{
		Enable:         true,
		APIKey:         "secret",
		APIKeyOptional: true,
		MaxBodySize:    1234,
	}

	got := directConfig(c)

	require.Equal(t, c.Enable, got.Enable)
	require.Equal(t, c.APIKey, got.APIKey)
	require.Equal(t, c.APIKeyOptional, got.APIKeyOptional)
	require.Equal(t, c.MaxBodySize, got.MaxBodySize)
}
