package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
)

// nopRW is a reusable no-op ResponseWriter so the benchmark measures the middleware
// overhead (statusRecorder wrap + ObserveHTTP) rather than per-iteration recorder allocs.
type nopRW struct{ h http.Header }

func (n *nopRW) Header() http.Header       { return n.h }
func (*nopRW) Write(b []byte) (int, error) { return len(b), nil }
func (*nopRW) WriteHeader(int)             {}

func benchInstrumentedHandler(m *metrics.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return instrumentHTTP(m, "internal", mux)
}

func benchServe(b *testing.B, m *metrics.Metrics) {
	b.Helper()
	h := benchInstrumentedHandler(m)
	req := httptest.NewRequest(http.MethodGet, "/healthy", nil)
	rw := &nopRW{h: make(http.Header)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(rw, req)
	}
}

// Enabled: full statusRecorder wrap + timing + ObserveHTTP per request.
func BenchmarkInstrumentHTTPEnabled(b *testing.B) {
	benchServe(b, metrics.New(metrics.Config{Enable: true, HTTP: true}))
}

// Group off: HTTPEnabled() is false, so instrumentHTTP returns the bare mux (passthrough).
func BenchmarkInstrumentHTTPGroupOff(b *testing.B) {
	benchServe(b, metrics.New(metrics.Config{Enable: true, HTTP: false}))
}

// Nil metrics: the disabled deployment — bare mux, zero metrics work.
func BenchmarkInstrumentHTTPNil(b *testing.B) {
	benchServe(b, nil)
}
