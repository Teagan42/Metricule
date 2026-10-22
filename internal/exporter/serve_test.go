package exporter_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/exporter"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

// The server listens on the configured address and shuts down when its context ends.
func TestListenAndServeAndShutdown(t *testing.T) {
	s, err := exporter.New(exporter.Options{
		Server: config.Server{
			Listen:        "127.0.0.1:0",
			MetricsPath:   config.DefaultMetricsPath,
			HealthPath:    config.DefaultHealthPath,
			ReadinessPath: config.DefaultReadinessPath,
		},
		Store:     newFakeStore(gaugeSnapshot()),
		Registry:  prometheus.NewRegistry(),
		Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.ListenAndServe(ctx) }()

	addr := waitForAddr(t, s)
	body := httpGet(t, "http://"+addr+config.DefaultMetricsPath)
	if body != wantJobsExposition {
		t.Errorf("exposition =\n%s\nwant\n%s", body, wantJobsExposition)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

// An unusable listen address is a startup failure, not a silent degradation.
func TestListenAndServeReportsBindFailure(t *testing.T) {
	s, err := exporter.New(exporter.Options{
		Server:    config.Server{Listen: "127.0.0.1:-1", MetricsPath: config.DefaultMetricsPath},
		Store:     newFakeStore(),
		Registry:  prometheus.NewRegistry(),
		Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.ListenAndServe(context.Background()); err == nil {
		t.Error("ListenAndServe succeeded on an invalid address")
	}
}

func waitForAddr(t *testing.T, s *exporter.Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := s.Addr(); addr != "" {
			return addr
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server never reported a bound address")
	return ""
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return string(body)
}
