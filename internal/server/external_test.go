package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyAPIKey(t *testing.T) {
	e := &External{direct: DirectConfig{APIKey: "test-secret-key"}}

	tests := []struct {
		name    string
		header  string
		wantErr bool
	}{
		{
			name:    "valid key",
			header:  "test-secret-key",
			wantErr: false,
		},
		{
			name:    "wrong key",
			header:  "wrong-key",
			wantErr: true,
		},
		{
			name:    "empty header",
			header:  "",
			wantErr: true,
		},
		{
			name:    "missing header",
			header:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/direct", nil)
			if tt.header != "" {
				req.Header.Set("X-API-Key", tt.header)
			}

			err := e.verifyAPIKey(req)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errUnauthorized)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVerifyAPIKeyNoAPIKey(t *testing.T) {
	e := &External{direct: DirectConfig{APIKey: "secret", NoAPIKey: true}}

	req := httptest.NewRequest(http.MethodPost, "/direct", nil)
	// No X-API-Key header set.

	// verifyAPIKey itself still rejects without a valid header.
	err := e.verifyAPIKey(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnauthorized)

	// The NoAPIKey flag causes directH to skip the verifyAPIKey call.
	assert.True(t, e.direct.NoAPIKey)
}
