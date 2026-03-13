package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/stretchr/testify/require"
)

func TestHandleError(t *testing.T) {
	rr := httptest.NewRecorder()

	handleError(rr, errors.New("random error"), false)

	require.Equal(t, 500, rr.Result().StatusCode)
	require.Equal(t, "text/plain; charset=utf-8", rr.Result().Header.Get("Content-Type"))

	reason := rr.Body.String()
	require.Equal(t, "internal processing error\n", reason)

	r400 := httptest.NewRecorder()
	err := fmt.Errorf("%w: some error", status.HTTP[400])

	for range 5 {
		err = fmt.Errorf("wrap %w", err)
	}

	handleError(r400, err, true)

	require.Equal(t, 400, r400.Result().StatusCode)
	require.Equal(t, "text/plain; charset=utf-8", r400.Result().Header.Get("Content-Type"))

	reason = r400.Body.String()
	require.Equal(t, fmt.Sprintf("%s\n", err.Error()), reason)
}

type mockLiveness struct {
	ready   bool
	startup bool
}

const (
	notReady   = "not ready"
	notStarted = "not started"
)

func (m *mockLiveness) Ready(ctx context.Context) error {
	if !m.ready {
		return errors.New(notReady)
	}
	return nil
}

func (m *mockLiveness) Startup(ctx context.Context) error {
	if !m.startup {
		return errors.New(notStarted)
	}
	return nil
}

func TestLiveness(t *testing.T) {
	ml := mockLiveness{
		ready:   false,
		startup: false,
	}
	mLiveness := livenessHandlers{&ml}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthy", nil)
	mLiveness.healthy(rr, req)
	require.Equal(t, http.StatusOK, rr.Result().StatusCode)

	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr = httptest.NewRecorder()

	mLiveness.ready(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Result().StatusCode)

	reason := rr.Body.String()
	require.Equal(t, notReady+"\n", reason)

	ml.ready = true
	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr = httptest.NewRecorder()

	mLiveness.ready(rr, req)
	require.Equal(t, http.StatusOK, rr.Result().StatusCode)

	ml.startup = false
	req = httptest.NewRequest(http.MethodGet, "/startup", nil)
	rr = httptest.NewRecorder()

	mLiveness.startup(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Result().StatusCode)

	reason = rr.Body.String()
	require.Equal(t, notStarted+"\n", reason)

	ml.startup = true
	req = httptest.NewRequest(http.MethodGet, "/startup", nil)
	rr = httptest.NewRecorder()

	mLiveness.startup(rr, req)
	require.Equal(t, http.StatusOK, rr.Result().StatusCode)
}
