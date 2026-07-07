package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
)

// TestInstrumentHTTPRecordsRoutePattern verifies that the route label is the mux's
// matched pattern (not the raw path) — which is only populated after the mux serves.
func TestInstrumentHTTPRecordsRoutePattern(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := instrumentHTTP(m, "internal", mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthy", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	const expected = `
# HELP teeproxy_http_requests_total HTTP requests by server, route, and status class.
# TYPE teeproxy_http_requests_total counter
teeproxy_http_requests_total{route="GET /healthy",server="internal",status_class="2xx"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_http_requests_total"))
}

// TestInstrumentHTTPImplicitOK verifies a handler that writes a body without calling
// WriteHeader is recorded as 2xx (the statusRecorder defaults to 200), not 1xx — the
// common case for JSON handlers that never set an explicit status.
func TestInstrumentHTTPImplicitOK(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) // no explicit WriteHeader
	})
	h := instrumentHTTP(m, "internal", mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/info", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	const expected = `
# HELP teeproxy_http_requests_total HTTP requests by server, route, and status class.
# TYPE teeproxy_http_requests_total counter
teeproxy_http_requests_total{route="GET /info",server="internal",status_class="2xx"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_http_requests_total"))
}

// TestInstrumentHTTPDisabledIsPassthrough verifies that a disabled HTTP group leaves
// the handler unwrapped (the same handler instance is returned).
func TestInstrumentHTTPDisabledIsPassthrough(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: false})

	mux := http.NewServeMux()
	require.Equal(t, http.Handler(mux), instrumentHTTP(m, "internal", mux))
	require.Equal(t, http.Handler(mux), instrumentHTTP(nil, "internal", mux))
}
