package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubResultService struct {
	response *types.ActionResponse
}

func (s *stubResultService) ProcessAndStore(context.Context, *types.ActionResponse) error {
	return nil
}

func (s *stubResultService) Serve(context.Context, common.Hash, types.SubmissionTag) (*types.ActionResponse, error) {
	return s.response, nil
}

// TestResultHProxySignatureUsesResultHash verifies that resultH populates
// ProxySignature by signing Result.Hash() (the canonical hash), not
// Keccak256(Result.Data) (the pre-ff16f9b path).
func TestResultHProxySignatureUsesResultHash(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	proxyAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	actionID := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa")
	resp := &types.ActionResponse{
		Result: types.ActionResult{
			ID:            actionID,
			SubmissionTag: types.Threshold,
			Status:        1,
			Data:          []byte(`{"ok":true}`),
		},
	}

	e := &External{
		resultService: &stubResultService{response: resp},
		privKey:       privKey,
	}

	req := httptest.NewRequest(http.MethodGet, "/action/result/"+actionID.Hex(), nil)
	req.SetPathValue("actionID", actionID.Hex())
	rr := httptest.NewRecorder()

	err = e.resultH(rr, req)
	require.NoError(t, err)

	var got types.ActionResponse
	err = json.NewDecoder(rr.Body).Decode(&got)
	require.NoError(t, err)
	require.NotEmpty(t, got.ProxySignature, "ProxySignature should be populated")

	canonicalHash := accounts.TextHash(got.Result.Hash())
	pub, err := crypto.SigToPub(canonicalHash, got.ProxySignature)
	require.NoError(t, err)
	assert.Equal(t, proxyAddr, crypto.PubkeyToAddress(*pub),
		"ProxySignature must recover to the proxy address when verified against Result.Hash()")

	legacyHash := accounts.TextHash(crypto.Keccak256(got.Result.Data))
	legacyPub, err := crypto.SigToPub(legacyHash, got.ProxySignature)
	if err == nil {
		assert.NotEqual(t, proxyAddr, crypto.PubkeyToAddress(*legacyPub),
			"ProxySignature must NOT recover under the legacy Keccak256(Data) path")
	}
}

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
