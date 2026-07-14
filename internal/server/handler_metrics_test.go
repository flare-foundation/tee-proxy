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

// TestInstrumentHTTPPanicBeforeWriteHeader verifies a handler that panics before writing
// any status is still recorded once as 5xx (with a duration sample) and the panic re-raised.
func TestInstrumentHTTPPanicBeforeWriteHeader(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /panic", func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	h := instrumentHTTP(m, "internal", mux)

	require.Panics(t, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	})

	const expected = `
# HELP teeproxy_http_requests_total HTTP requests by server, route, and status class.
# TYPE teeproxy_http_requests_total counter
teeproxy_http_requests_total{route="GET /panic",server="internal",status_class="5xx"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_http_requests_total"))
	require.Equal(t, 1, testutil.CollectAndCount(m.Registry(), "teeproxy_http_request_duration_seconds"))
}

// TestInstrumentHTTPPanicAfterPartialWrite verifies that a panic after a 200 has already
// been written still records 5xx (panic status takes precedence) and no 2xx series appears.
func TestInstrumentHTTPPanicAfterPartialWrite(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /partial", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	})
	h := instrumentHTTP(m, "internal", mux)

	require.Panics(t, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/partial", nil))
	})

	const expected = `
# HELP teeproxy_http_requests_total HTTP requests by server, route, and status class.
# TYPE teeproxy_http_requests_total counter
teeproxy_http_requests_total{route="GET /partial",server="internal",status_class="5xx"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_http_requests_total"))
}

// TestInstrumentHTTPErrAbortHandler verifies http.ErrAbortHandler is re-raised unchanged
// (passthrough) while still recording one 5xx sample, preserving one-sample-per-request.
func TestInstrumentHTTPErrAbortHandler(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /abort", func(_ http.ResponseWriter, _ *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := instrumentHTTP(m, "internal", mux)

	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	})

	const expected = `
# HELP teeproxy_http_requests_total HTTP requests by server, route, and status class.
# TYPE teeproxy_http_requests_total counter
teeproxy_http_requests_total{route="GET /abort",server="internal",status_class="5xx"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_http_requests_total"))
}

// TestInstrumentHTTPNoDoubleObservationOnSuccess guards against the deferred observation
// double-counting or dropping a normal request: a 200 records exactly one 2xx count and
// one duration sample.
func TestInstrumentHTTPNoDoubleObservationOnSuccess(t *testing.T) {
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
	require.Equal(t, 1, testutil.CollectAndCount(m.Registry(), "teeproxy_http_request_duration_seconds"))
}
