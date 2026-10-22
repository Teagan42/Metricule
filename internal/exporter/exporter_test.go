package exporter_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/exporter"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

// §12.1 exposition, verbatim from the specification.
const wantJobsExposition = `# HELP app_jobs_by_status Current job count by status
# TYPE app_jobs_by_status gauge
app_jobs_by_status{status="failed"} 2
app_jobs_by_status{status="pending"} 12
app_jobs_by_status{status="running"} 4
`

// §8.1: the scrape handler renders prepared snapshots.
func TestMetricsServesPreparedSnapshot(t *testing.T) {
	h := newHandler(t, newFakeStore(gaugeSnapshot()), nil)

	body, code := get(t, h, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body != wantJobsExposition {
		t.Errorf("exposition =\n%s\nwant\n%s", body, wantJobsExposition)
	}
}

// §8.5: a counter family exposes the counter wire type.
func TestMetricsExposesCounterType(t *testing.T) {
	store := newFakeStore(&snapshot.Snapshot{
		CollectionID: "appdb-jobs",
		Families: []snapshot.Family{{
			Name:    "app_jobs_processed_total",
			Help:    "Jobs processed since boot",
			Type:    snapshot.TypeCounter,
			Samples: []snapshot.Sample{{Value: 7}},
		}},
	})

	want := `# HELP app_jobs_processed_total Jobs processed since boot
# TYPE app_jobs_processed_total counter
app_jobs_processed_total 7
`
	if body, _ := get(t, newHandler(t, store, nil), "/metrics"); body != want {
		t.Errorf("exposition =\n%s\nwant\n%s", body, want)
	}
}

// Exposition ordering must not depend on snapshot or map iteration order.
func TestMetricsOrderingIsDeterministic(t *testing.T) {
	store := newFakeStore(
		&snapshot.Snapshot{CollectionID: "b", Families: []snapshot.Family{{
			Name: "app_zeta", Help: "z", Type: snapshot.TypeGauge,
			Samples: []snapshot.Sample{
				{Labels: []snapshot.Label{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}}, Value: 1},
				{Labels: []snapshot.Label{{Name: "a", Value: "0"}, {Name: "b", Value: "9"}}, Value: 2},
			},
		}}},
		&snapshot.Snapshot{CollectionID: "a", Families: []snapshot.Family{{
			Name: "app_alpha", Help: "a", Type: snapshot.TypeGauge,
			Samples: []snapshot.Sample{{Value: 3}},
		}}},
	)
	h := newHandler(t, store, nil)

	want := `# HELP app_alpha a
# TYPE app_alpha gauge
app_alpha 3
# HELP app_zeta z
# TYPE app_zeta gauge
app_zeta{a="0",b="9"} 2
app_zeta{a="1",b="2"} 1
`
	first, _ := get(t, h, "/metrics")
	if first != want {
		t.Errorf("exposition =\n%s\nwant\n%s", first, want)
	}
	if second, _ := get(t, h, "/metrics"); second != first {
		t.Errorf("second scrape differs:\n%s", second)
	}
}

// §8.3: a labelset published twice must not appear twice in the exposition.
func TestMetricsEmitsNoDuplicateSeries(t *testing.T) {
	store := newFakeStore(gaugeSnapshot(), gaugeSnapshot())

	body, code := get(t, newHandler(t, store, nil), "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if n := countLines(body, `app_jobs_by_status{status="pending"}`); n != 1 {
		t.Errorf("series appears %d times, want 1:\n%s", n, body)
	}
}

// Exporter-owned telemetry shares the scrape response (§13).
func TestMetricsIncludesExporterTelemetry(t *testing.T) {
	reg := prometheus.NewRegistry()
	tel := telemetry.New(reg, version.Get())
	h := newHandlerWith(t, exporter.Options{
		Server:    serverConfig(),
		Store:     newFakeStore(gaugeSnapshot()),
		Registry:  reg,
		Telemetry: tel,
	})

	body, _ := get(t, h, "/metrics")
	for _, want := range []string{"metricule_build_info", wantJobsExposition} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition omits %q:\n%s", want, body)
		}
	}
}

// Only the configured paths are routed (§5.1 server block).
func TestConfiguredPathsAreRouted(t *testing.T) {
	srv := config.Server{
		Listen:        "127.0.0.1:0",
		MetricsPath:   "/custom/metrics",
		HealthPath:    "/custom/healthz",
		ReadinessPath: "/custom/readyz",
	}
	h := newHandlerWith(t, exporter.Options{
		Server:    srv,
		Store:     newFakeStore(gaugeSnapshot()),
		Registry:  prometheus.NewRegistry(),
		Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get()),
	})

	for path, wantCode := range map[string]int{
		"/custom/metrics": http.StatusOK,
		"/custom/healthz": http.StatusOK,
		"/custom/readyz":  http.StatusOK,
		"/metrics":        http.StatusNotFound,
		"/":               http.StatusNotFound,
	} {
		if _, code := get(t, h, path); code != wantCode {
			t.Errorf("GET %s = %d, want %d", path, code, wantCode)
		}
	}
}

func serverConfig() config.Server {
	return config.Server{
		Listen:        "127.0.0.1:0",
		MetricsPath:   config.DefaultMetricsPath,
		HealthPath:    config.DefaultHealthPath,
		ReadinessPath: config.DefaultReadinessPath,
	}
}

func newHandler(t *testing.T, store *fakeStore, ready exporter.Readiness) http.Handler {
	t.Helper()
	return newHandlerWith(t, exporter.Options{
		Server:    serverConfig(),
		Store:     store,
		Registry:  prometheus.NewRegistry(),
		Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get()),
		Readiness: ready,
	})
}

func newHandlerWith(t *testing.T, opts exporter.Options) http.Handler {
	t.Helper()
	s, err := exporter.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s.Handler()
}

func get(t *testing.T, h http.Handler, path string) (string, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String(), rec.Code
}

func countLines(body, prefix string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}
