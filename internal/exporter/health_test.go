package exporter_test

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/teagan42/metricule/internal/exporter"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

// §13.1: liveness does not require a healthy source.
func TestHealthzIgnoresSourceHealth(t *testing.T) {
	h := newHandler(t, newFakeStore(), exporter.ReadinessFunc(func() error {
		return errors.New("source appdb unreachable")
	}))

	body, code := get(t, h, "/healthz")
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "ok") {
		t.Errorf("body = %q, want ok", body)
	}
}

// §13.1: readiness reflects compiled configuration and initialized subsystems.
func TestReadyzReportsSubsystemState(t *testing.T) {
	notReady := errors.New("source manager not initialized")
	ready := false
	h := newHandler(t, newFakeStore(), exporter.ReadinessFunc(func() error {
		if ready {
			return nil
		}
		return notReady
	}))

	if body, code := get(t, h, "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d (%q), want 503", code, body)
	}

	ready = true
	if body, code := get(t, h, "/readyz"); code != http.StatusOK {
		t.Errorf("status = %d (%q), want 200", code, body)
	}
}

// A nil probe means ready: the documented policy is that readiness covers startup,
// not the first collection.
func TestReadyzDefaultsToReady(t *testing.T) {
	if _, code := get(t, newHandler(t, newFakeStore(), nil), "/readyz"); code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
}

// §13: every endpoint records metricule_http_* under its configured path.
func TestHTTPTelemetryRecordsBoundedPaths(t *testing.T) {
	telReg := prometheus.NewRegistry()
	tel := telemetry.New(telReg, version.Get())
	s, err := exporter.New(exporter.Options{
		Server:    serverConfig(),
		Store:     newFakeStore(gaugeSnapshot()),
		Registry:  prometheus.NewRegistry(),
		Telemetry: tel,
		Readiness: exporter.ReadinessFunc(func() error { return errors.New("starting") }),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := s.Handler()

	get(t, h, "/metrics")
	get(t, h, "/healthz")
	get(t, h, "/readyz")

	want := []string{
		`code="200" path="/healthz" 1`,
		`code="200" path="/metrics" 1`,
		`code="503" path="/readyz" 1`,
	}
	got := counterSeries(t, telReg, "metricule_http_requests_total")
	if len(got) != len(want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("series[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Renders one counter family's series, sorted, as label pairs plus value.
func counterSeries(t *testing.T, g prometheus.Gatherer, name string) []string {
	t.Helper()
	families, err := g.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var out []string
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			out = append(out, renderSeries(m))
		}
	}
	sort.Strings(out)
	return out
}

func renderSeries(m *dto.Metric) string {
	var b strings.Builder
	for _, l := range m.GetLabel() {
		fmt.Fprintf(&b, "%s=%q ", l.GetName(), l.GetValue())
	}
	fmt.Fprintf(&b, "%g", m.GetCounter().GetValue())
	return b.String()
}

// An unrouted path must not become a telemetry label.
func TestUnroutedPathIsNotCounted(t *testing.T) {
	telReg := prometheus.NewRegistry()
	s, err := exporter.New(exporter.Options{
		Server:    serverConfig(),
		Store:     newFakeStore(),
		Registry:  prometheus.NewRegistry(),
		Telemetry: telemetry.New(telReg, version.Get()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	get(t, s.Handler(), "/wp-login.php")

	if got := counterSeries(t, telReg, "metricule_http_requests_total"); len(got) != 0 {
		t.Errorf("http request series = %v, want none", got)
	}
}

// New refuses to build a server that cannot serve prepared state.
func TestNewRequiresCollaborators(t *testing.T) {
	cases := map[string]exporter.Options{
		"no store":     {Server: serverConfig(), Registry: prometheus.NewRegistry(), Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get())},
		"no registry":  {Server: serverConfig(), Store: newFakeStore(), Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get())},
		"no telemetry": {Server: serverConfig(), Store: newFakeStore(), Registry: prometheus.NewRegistry()},
	}
	for name, opts := range cases {
		if _, err := exporter.New(opts); err == nil {
			t.Errorf("%s: New succeeded, want error", name)
		}
	}
}
