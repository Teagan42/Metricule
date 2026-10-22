package main_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// End-to-end exercise of the M1 exit condition: a configured metric is collected
// from a real source by the real binary and served on /metrics, with no application
// change and no SQL or upstream request on the scrape path.
//
// Runs against the built binary so the composition root is covered — an adapter that
// is implemented but not linked produces a working test suite and a useless program.

// Reserves a loopback port by binding and releasing it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Starts the exporter and returns its base URL. Stops it on cleanup.
func startServe(t *testing.T, configBody string) string {
	t.Helper()

	port := freePort(t)
	body := strings.ReplaceAll(configBody, "{{LISTEN}}", fmt.Sprintf("127.0.0.1:%d", port))
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binary, "serve", "--config", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}

	t.Cleanup(func() {
		// SIGTERM rather than Kill: graceful shutdown is part of the contract.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil && !strings.Contains(err.Error(), "signal") {
				t.Errorf("serve exited with %v\noutput:\n%s", err, stderr.String())
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("serve did not shut down within 10s\noutput:\n%s", stderr.String())
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, base+"/healthz", &stderr)
	return base
}

// get issues a request and fully drains the body.
//
// Draining matters: a response body closed before it is read leaves the connection
// in a state the server still accounts for, which stalls graceful shutdown and
// makes the cleanup assertion fail for a reason that has nothing to do with the
// code under test.
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // bounded by caller deadlines
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(b)
}

func waitReady(t *testing.T, url string, out *strings.Builder) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := get(t, url); code == http.StatusOK {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("exporter never became healthy at %s\noutput:\n%s", url, out.String())
}

// Polls until the exposition satisfies want, so the test does not depend on
// collection having finished before the first scrape.
func awaitMetrics(t *testing.T, base string, want func(string) bool) string {
	t.Helper()
	var last string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		last = scrape(t, base)
		if want(last) {
			return last
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("exposition never satisfied the condition; last scrape:\n%s", last)
	return ""
}

func scrape(t *testing.T, base string) string {
	t.Helper()
	_, body := get(t, base+"/metrics")
	return body
}

const httpSourceConfig = `apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: "{{LISTEN}}"
sources:
  app:
    type: http
    base_url: {{UPSTREAM}}
    timeout: 2s
metrics:
  - name: app_active_sessions
    help: Currently active application sessions
    type: gauge
    source: app
    request:
      path: /api/status
    mapping:
      value: "$.sessions.active"
    collect:
      mode: poll
      interval: 500ms
      timeout: 1s
  - name: app_worker_tasks
    help: Active tasks per worker
    type: gauge
    source: app
    request:
      path: /api/workers
    mapping:
      rows: "$.workers[*]"
      value: "$.active_tasks"
      labels:
        worker: "$.name"
    collect:
      mode: poll
      interval: 500ms
      timeout: 1s
`

// Serves the upstream application the exporter reads, counting requests so the
// scrape-isolation assertion has something to measure.
func upstream(t *testing.T, sessions *atomic.Int64, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessions": map[string]any{"active": sessions.Load()},
		})
	})
	mux.HandleFunc("/api/workers", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workers": []any{
				map[string]any{"name": "worker-a", "active_tasks": 3},
				map[string]any{"name": "worker-b", "active_tasks": 0},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func serveConfig(upstreamURL string) string {
	return strings.ReplaceAll(httpSourceConfig, "{{UPSTREAM}}", upstreamURL)
}

func TestServeCollectsAndExposesAGauge(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(7)
	up := upstream(t, &sessions, &hits)

	base := startServe(t, serveConfig(up.URL))

	body := awaitMetrics(t, base, func(s string) bool {
		return strings.Contains(s, "app_active_sessions 7")
	})

	// Declared metadata must survive into the exposition.
	if !strings.Contains(body, "# HELP app_active_sessions Currently active application sessions") {
		t.Errorf("missing HELP line:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE app_active_sessions gauge") {
		t.Errorf("missing TYPE line:\n%s", body)
	}
}

// Label extraction over a JSON array, including a legitimate zero value.
func TestServeExposesLabelledSeries(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(1)
	up := upstream(t, &sessions, &hits)

	base := startServe(t, serveConfig(up.URL))

	body := awaitMetrics(t, base, func(s string) bool {
		return strings.Contains(s, `app_worker_tasks{worker="worker-a"} 3`)
	})

	// Zero is a real value here, not a missing one.
	if !strings.Contains(body, `app_worker_tasks{worker="worker-b"} 0`) {
		t.Errorf("missing zero-valued series:\n%s", body)
	}
}

// A refreshed source value must reach the exposition without a restart.
func TestServeRefreshesOnItsOwnSchedule(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(7)
	up := upstream(t, &sessions, &hits)

	base := startServe(t, serveConfig(up.URL))
	awaitMetrics(t, base, func(s string) bool { return strings.Contains(s, "app_active_sessions 7") })

	sessions.Store(42)
	awaitMetrics(t, base, func(s string) bool { return strings.Contains(s, "app_active_sessions 42") })
}

// P2: scraping must not drive collection. Repeated scrapes with collection paused
// must not produce upstream requests.
func TestScrapingDoesNotTriggerUpstreamRequests(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(5)
	up := upstream(t, &sessions, &hits)

	// A long interval means at most the initial collection happens on its own.
	cfg := strings.ReplaceAll(serveConfig(up.URL), "interval: 500ms", "interval: 1h")
	base := startServe(t, cfg)
	awaitMetrics(t, base, func(s string) bool { return strings.Contains(s, "app_active_sessions 5") })

	before := hits.Load()
	for i := 0; i < 20; i++ {
		scrape(t, base)
	}
	if after := hits.Load(); after != before {
		t.Errorf("20 scrapes caused %d upstream requests; scrapes must read a snapshot only (P2)", after-before)
	}
}

// Exporter-owned telemetry must be exposed alongside configured metrics (§13).
func TestServeExposesItsOwnTelemetry(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(1)
	up := upstream(t, &sessions, &hits)

	base := startServe(t, serveConfig(up.URL))
	body := awaitMetrics(t, base, func(s string) bool {
		return strings.Contains(s, "metricule_collection_success")
	})

	for _, family := range []string{
		"metricule_build_info",
		"metricule_source_up",
		"metricule_collection_success",
		"metricule_collection_duration_seconds",
		"metricule_collection_samples",
		"metricule_http_requests_total",
	} {
		if !strings.Contains(body, family) {
			t.Errorf("missing exporter telemetry family %s", family)
		}
	}
}

func TestHealthAndReadinessEndpoints(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(1)
	up := upstream(t, &sessions, &hits)
	base := startServe(t, serveConfig(up.URL))

	for _, path := range []string{"/healthz", "/readyz"} {
		code, _ := get(t, base+path)
		if code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, code)
		}
	}
}

// An unreachable source must not stop the exporter serving, and must not fabricate
// a zero (P3, AD-010).
func TestUnreachableSourceKeepsExporterServing(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation and is unroutable.
	cfg := strings.ReplaceAll(serveConfig("http://192.0.2.1:9"), "interval: 500ms", "interval: 500ms")
	base := startServe(t, cfg)

	body := awaitMetrics(t, base, func(s string) bool {
		return strings.Contains(s, "metricule_collection_errors_total")
	})

	if strings.Contains(body, "app_active_sessions 0") {
		t.Error("an unreachable source must not publish a zero sample")
	}
	if strings.Contains(body, "app_active_sessions ") {
		t.Errorf("no sample should exist before a first successful collection:\n%s", body)
	}
}

// `metricule test` must run one collection and print the resulting exposition.
func TestTestCommandCollectsOnce(t *testing.T) {
	var sessions, hits atomic.Int64
	sessions.Store(9)
	up := upstream(t, &sessions, &hits)

	body := strings.ReplaceAll(serveConfig(up.URL), "{{LISTEN}}", "127.0.0.1:0")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := run(t, "test", "--config", path, "--metric", "app_active_sessions")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "app_active_sessions 9") {
		t.Errorf("stdout should contain the collected sample:\n%s", got.stdout)
	}
}
