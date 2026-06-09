package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubResultService struct {
	response *types.ActionResponse
	serveErr error
}

func (s *stubResultService) ProcessAndStore(context.Context, *types.ActionResponse) error {
	return nil
}

func (s *stubResultService) Serve(context.Context, common.Hash, types.SubmissionTag) (*types.ActionResponse, error) {
	if s.serveErr != nil {
		return nil, s.serveErr
	}
	return s.response, nil
}

// TestResultHProxySignatureUsesDomainPreimage verifies that resultH signs the
// domain-separated PROXY_ACTION_RESULT preimage over Result.Hash().
func TestResultHProxySignatureUsesDomainPreimage(t *testing.T) {
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

	const testChainID = uint64(14)
	e := &External{
		resultService: &stubResultService{response: resp},
		privKey:       privKey,
		chainID:       testChainID,
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

	signHash, err := csigning.NewPayload(csigning.ProxyActionResult, testChainID, common.BytesToHash(got.Result.Hash())).Hash()
	require.NoError(t, err)
	canonicalHash := accounts.TextHash(signHash[:])
	pub, err := crypto.SigToPub(canonicalHash, got.ProxySignature)
	require.NoError(t, err)
	assert.Equal(t, proxyAddr, crypto.PubkeyToAddress(*pub),
		"ProxySignature must recover to the proxy address under the PROXY_ACTION_RESULT domain preimage")

	legacyHash := accounts.TextHash(got.Result.Hash())
	legacyPub, err := crypto.SigToPub(legacyHash, got.ProxySignature)
	if err == nil {
		assert.NotEqual(t, proxyAddr, crypto.PubkeyToAddress(*legacyPub),
			"ProxySignature must NOT recover under the bare (undomained) Result.Hash() path")
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

func TestVerifyAPIKeyAPIKeyOptional(t *testing.T) {
	e := &External{direct: DirectConfig{APIKey: "secret", APIKeyOptional: true}}

	req := httptest.NewRequest(http.MethodPost, "/direct", nil)
	// No X-API-Key header set.

	// verifyAPIKey itself still rejects without a valid header.
	err := e.verifyAPIKey(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnauthorized)

	// The APIKeyOptional flag causes directH to skip the verifyAPIKey call.
	assert.True(t, e.direct.APIKeyOptional)
}

func TestSubmissionTagParam(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    types.SubmissionTag
		wantErr error
	}{
		{name: "missing defaults to threshold", query: "", want: types.Threshold},
		{name: "end", query: "submissionTag=end", want: types.End},
		{name: "submit", query: "submissionTag=submit", want: types.Submit},
		{name: "threshold", query: "submissionTag=threshold", want: types.Threshold},
		{name: "unknown rejected", query: "submissionTag=bogus", wantErr: errInvalidSubmissionTag},
		{name: "multiple values rejected", query: "submissionTag=end&submissionTag=submit", wantErr: errEmptySubmissionTag},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil)
			require.NoError(t, req.ParseForm())

			got, err := submissionTagParam(req)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateDirect(t *testing.T) {
	tests := []struct {
		name    string
		opType  common.Hash
		wantErr error
	}{
		{name: "system F_WALLET rejected", opType: op.Wallet.Hash(), wantErr: errSystemDirect},
		{name: "system F_GET rejected", opType: op.Get.Hash(), wantErr: errSystemDirect},
		{name: "non-system custom type accepted", opType: op.Type("F_CUSTOM").Hash()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDirect(&types.DirectInstruction{OPType: tt.opType})
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func resultHRequest(actionIDHex, rawQuery string) *http.Request {
	url := "/action/result/" + actionIDHex
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("actionID", actionIDHex)
	return req
}

func TestResultHErrorPaths(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	validID := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa")

	t.Run("service error propagates", func(t *testing.T) {
		notFound := fmt.Errorf("%w: nothing here", status.HTTP[http.StatusNotFound])
		e := &External{
			resultService: &stubResultService{serveErr: notFound},
			privKey:       privKey,
		}

		req := resultHRequest(validID.Hex(), "")
		err := e.resultH(httptest.NewRecorder(), req)
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, status.ErrToCode(err))
	})

	t.Run("invalid actionID rejected", func(t *testing.T) {
		e := &External{
			resultService: &stubResultService{},
			privKey:       privKey,
		}

		req := resultHRequest("not-a-hash", "")
		err := e.resultH(httptest.NewRecorder(), req)
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, status.ErrToCode(err))
	})

	t.Run("invalid submissionTag rejected", func(t *testing.T) {
		e := &External{
			resultService: &stubResultService{},
			privKey:       privKey,
		}

		req := resultHRequest(validID.Hex(), "submissionTag=bogus")
		err := e.resultH(httptest.NewRecorder(), req)
		require.Error(t, err)
		assert.ErrorIs(t, err, errInvalidSubmissionTag)
	})
}

// TestPrepareHandlerMaxBytesReturns413 verifies oversized bodies surface as 413, not 400/500.
func TestPrepareHandlerMaxBytesReturns413(t *testing.T) {
	const maxBodySize int64 = 16

	// Handler reads the full body so MaxBytesReader has a chance to trip.
	handler := prepareHandler(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := io.ReadAll(r.Body)
		return err
	}, maxBodySize, false)

	body := strings.Repeat("x", int(maxBodySize)+1)
	req := httptest.NewRequest(http.MethodPost, "/whatever", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
