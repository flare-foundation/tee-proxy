package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
)

// TestMetricsRouteEnabled200 guards that NewInternal mounts /metrics when the metrics
// group is enabled, and that the exposed body carries series already recorded by
// instrumentHTTP for other requests served on the same handler.
func TestMetricsRouteEnabled200(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})
	i := NewInternal("0", nil, nil, nil, &mockLiveness{ready: true, startup: true}, m)

	rec := httptest.NewRecorder()
	i.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthy", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	i.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "teeproxy_http_requests_total")
}

// TestMetricsRouteOpenMetricsNegotiation guards promhttp's EnableOpenMetrics content
// negotiation: an OpenMetrics Accept header gets an OpenMetrics response, and a
// plain-text Accept header (the default Prometheus scrape) gets the text exposition format.
func TestMetricsRouteOpenMetricsNegotiation(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, HTTP: true})
	i := NewInternal("0", nil, nil, nil, &mockLiveness{ready: true, startup: true}, m)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0;charset=utf-8")
	rec := httptest.NewRecorder()
	i.server.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	contentType := rec.Header().Get("Content-Type")
	require.True(t, strings.HasPrefix(contentType, "application/openmetrics-text"),
		"got Content-Type %q", contentType)

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain")
	rec = httptest.NewRecorder()
	i.server.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	contentType = rec.Header().Get("Content-Type")
	require.True(t, strings.HasPrefix(contentType, "text/plain"),
		"got Content-Type %q", contentType)
}

// TestMetricsRouteDisabled404 guards that /metrics is not mounted at all when metrics are
// disabled or absent, so a scraper hitting a misconfigured deployment gets a plain 404
// rather than an empty or error body.
func TestMetricsRouteDisabled404(t *testing.T) {
	disabled := metrics.New(metrics.Config{Enable: false})
	iDisabled := NewInternal("0", nil, nil, nil, &mockLiveness{ready: true, startup: true}, disabled)

	rec := httptest.NewRecorder()
	iDisabled.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	iNil := NewInternal("0", nil, nil, nil, &mockLiveness{ready: true, startup: true}, nil)

	rec = httptest.NewRecorder()
	iNil.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
