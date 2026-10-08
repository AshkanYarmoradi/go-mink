package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsAddr_IsLoopbackOnly(t *testing.T) {
	host, port, err := net.SplitHostPort(metricsAddr)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host, "metrics endpoint must bind loopback, not every interface")
	assert.Equal(t, "9090", port)
}

func TestNewMetricsServer_DedicatedMuxWithTimeouts(t *testing.T) {
	srv := newMetricsServer("127.0.0.1:0", http.NotFoundHandler())
	require.NotNil(t, srv)

	assert.Equal(t, "127.0.0.1:0", srv.Addr)
	assert.NotNil(t, srv.Handler, "server must not fall back to http.DefaultServeMux")
	assert.NotSame(t, http.DefaultServeMux, srv.Handler)

	assert.Positive(t, srv.ReadHeaderTimeout, "ReadHeaderTimeout guards against Slowloris")
	assert.Positive(t, srv.ReadTimeout)
	assert.Positive(t, srv.WriteTimeout)
	assert.Positive(t, srv.IdleTimeout)
}

func TestNewMetricsServer_ServesOnlyMetrics(t *testing.T) {
	// Register something on a fresh mux installed as the process default (and
	// restored on cleanup) to prove the metrics server does not expose it. A
	// server with a nil Handler would fall back to http.DefaultServeMux and leak
	// it. Registering the pattern directly on the process-wide default mux would
	// panic the second time this test runs in one process (go test -count=2).
	leakMux := http.NewServeMux()
	leakMux.HandleFunc("/debug/leak", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("leak"))
	})
	previous := http.DefaultServeMux
	http.DefaultServeMux = leakMux
	t.Cleanup(func() { http.DefaultServeMux = previous })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := newMetricsServer(ln.Addr().String(), promhttp.Handler())
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + ln.Addr().String()

	resp, err := client.Get(base + "/metrics")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.Contains(string(body), "# HELP") || strings.Contains(string(body), "# TYPE"),
		"expected Prometheus exposition format")

	resp, err = client.Get(base + "/debug/leak")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "default-mux handlers must not be reachable")
}
