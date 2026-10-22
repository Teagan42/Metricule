package telemetry_test

import (
	"sort"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

// Label names permitted on exporter-owned families.
//
// §13 requires bounded identifiers. Any label outside this set is a cardinality
// hazard and must be justified by extending this list deliberately.
var allowedLabels = map[string]bool{
	"source":     true,
	"collection": true,
	"reason":     true,
	"result":     true,
	"trigger":    true,
	"mode":       true,
	"path":       true,
	"code":       true,
	// build_info carries only constant, per-process values.
	"version": true,
	"commit":  true,
	"go":      true,
}

// Label names that would carry free text and so must never appear.
var forbiddenLabels = []string{"error", "message", "detail", "query", "sql", "value", "dsn", "url"}

// Expected families and types from §13. Stable family definitions are a contract:
// renaming one breaks every dashboard and alert built on it.
var wantFamilies = map[string]dto.MetricType{
	"metricule_build_info":                                dto.MetricType_GAUGE,
	"metricule_source_up":                                 dto.MetricType_GAUGE,
	"metricule_collection_success":                        dto.MetricType_GAUGE,
	"metricule_collection_duration_seconds":               dto.MetricType_HISTOGRAM,
	"metricule_collection_errors_total":                   dto.MetricType_COUNTER,
	"metricule_collection_refresh_total":                  dto.MetricType_COUNTER,
	"metricule_collection_last_success_timestamp_seconds": dto.MetricType_GAUGE,
	"metricule_collection_snapshot_age_seconds":           dto.MetricType_GAUGE,
	"metricule_collection_samples":                        dto.MetricType_GAUGE,
	"metricule_collection_trigger_events_total":           dto.MetricType_COUNTER,
	"metricule_collection_coalesced_events_total":         dto.MetricType_COUNTER,
	"metricule_source_reconnects_total":                   dto.MetricType_COUNTER,
	"metricule_mapping_errors_total":                      dto.MetricType_COUNTER,
	"metricule_cardinality_rejections_total":              dto.MetricType_COUNTER,
	"metricule_http_requests_total":                       dto.MetricType_COUNTER,
	"metricule_http_request_duration_seconds":             dto.MetricType_HISTOGRAM,
}

// Drives every family so each appears in the exposition.
//
// A labeled family stays absent from Gather until it has a child, so presence can
// only be asserted by exercising the recording API.
func exercised(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := telemetry.New(reg, version.Info{Version: "v0.1.0", Commit: "abc123", GoVersion: "go1.27.0"})

	m.SetSourceUp("appdb", true)
	m.SourceReconnected("appdb")

	m.CollectionSucceeded("app_jobs_pending", "appdb", 150*time.Millisecond, 3, time.Now())
	m.CollectionFailed("app_jobs_by_status", "appdb", 2*time.Second, telemetry.ReasonTimeout)
	m.RefreshRequested("app_jobs_pending", telemetry.TriggerSchedule)
	m.EventsCoalesced("app_jobs_by_status", 7)
	m.TriggerEventReceived("appdb", "app_jobs_by_status")
	m.MappingFailed("app_jobs_pending", telemetry.ReasonNullValue)
	m.CardinalityRejected("app_jobs_by_status")
	m.SetSnapshotAge("app_jobs_pending", 12*time.Second)

	m.ObserveScrape("/metrics", 200, 4*time.Millisecond)
	return reg
}

func gather(t *testing.T, reg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

func TestAllSpecifiedFamiliesAreExposed(t *testing.T) {
	got := gather(t, exercised(t))

	for _, name := range sortedNames(wantFamilies) {
		f, ok := got[name]
		if !ok {
			t.Errorf("family %s is missing from the exposition", name)
			continue
		}
		if f.GetType() != wantFamilies[name] {
			t.Errorf("family %s type = %s, want %s", name, f.GetType(), wantFamilies[name])
		}
		if f.GetHelp() == "" {
			t.Errorf("family %s has no help text", name)
		}
	}
}

// Guards §13: no exporter-owned family may carry an unbounded label.
func TestFamilyLabelsAreBounded(t *testing.T) {
	for name, f := range gather(t, exercised(t)) {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if !allowedLabels[l.GetName()] {
					t.Errorf("family %s has unexpected label %q; bounded labels are %v",
						name, l.GetName(), sortedKeys(allowedLabels))
				}
				for _, forbidden := range forbiddenLabels {
					if l.GetName() == forbidden {
						t.Errorf("family %s carries free-text label %q", name, forbidden)
					}
				}
			}
		}
	}
}

func TestBuildInfoReportsVersion(t *testing.T) {
	f, ok := gather(t, exercised(t))["metricule_build_info"]
	if !ok {
		t.Fatal("metricule_build_info is missing")
	}
	if len(f.GetMetric()) != 1 {
		t.Fatalf("build_info should have exactly one series, got %d", len(f.GetMetric()))
	}

	m := f.GetMetric()[0]
	if m.GetGauge().GetValue() != 1 {
		t.Errorf("build_info value = %v, want 1", m.GetGauge().GetValue())
	}
	labels := map[string]string{}
	for _, l := range m.GetLabel() {
		labels[l.GetName()] = l.GetValue()
	}
	if labels["version"] != "v0.1.0" {
		t.Errorf("version label = %q, want v0.1.0", labels["version"])
	}
	if labels["commit"] != "abc123" {
		t.Errorf("commit label = %q, want abc123", labels["commit"])
	}
}

// A failed collection must be distinguishable from a successful one, and from a
// collection that has never run. Never zeroing is P3.
func TestCollectionSuccessDistinguishesOutcomes(t *testing.T) {
	got := gather(t, exercised(t))

	success := seriesByLabel(t, got, "metricule_collection_success", "collection")
	if v := success["app_jobs_pending"]; v != 1 {
		t.Errorf("successful collection reports %v, want 1", v)
	}
	if v := success["app_jobs_by_status"]; v != 0 {
		t.Errorf("failed collection reports %v, want 0", v)
	}

	errs := seriesByLabel(t, got, "metricule_collection_errors_total", "collection")
	if v := errs["app_jobs_by_status"]; v != 1 {
		t.Errorf("error counter = %v, want 1", v)
	}
	if _, present := errs["app_jobs_pending"]; present {
		t.Error("a successful collection must not create an error series")
	}
}

func TestSnapshotAgeIsReported(t *testing.T) {
	ages := seriesByLabel(t, gather(t, exercised(t)), "metricule_collection_snapshot_age_seconds", "collection")
	if got := ages["app_jobs_pending"]; got != 12 {
		t.Errorf("snapshot age = %v, want 12", got)
	}
}

func TestCoalescedEventsAccumulate(t *testing.T) {
	v := seriesByLabel(t, gather(t, exercised(t)), "metricule_collection_coalesced_events_total", "collection")
	if got := v["app_jobs_by_status"]; got != 7 {
		t.Errorf("coalesced events = %v, want 7", got)
	}
}

// Reads the single numeric value of each series, keyed by one label's value.
func seriesByLabel(t *testing.T, families map[string]*dto.MetricFamily, family, label string) map[string]float64 {
	t.Helper()
	f, ok := families[family]
	if !ok {
		t.Fatalf("family %s is missing", family)
	}
	out := map[string]float64{}
	for _, m := range f.GetMetric() {
		key := ""
		for _, l := range m.GetLabel() {
			if l.GetName() == label {
				key = l.GetValue()
			}
		}
		switch {
		case m.GetGauge() != nil:
			out[key] = m.GetGauge().GetValue()
		case m.GetCounter() != nil:
			out[key] = m.GetCounter().GetValue()
		}
	}
	return out
}

func sortedNames(m map[string]dto.MetricType) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
