package mapping_test

import (
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

var collectedAt = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// §7.3: metrics sharing one query publish from one result set.
func TestGroupMapsEveryFamily(t *testing.T) {
	g := mustCompileGroup(t, mapping.GroupSpec{
		CollectionID: "appdb-jobs",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metrics: []config.Metric{
			{
				Name:    "app_jobs_by_status",
				Type:    config.MetricGauge,
				Mapping: config.Mapping{Value: "value", Labels: map[string]string{"status": "status"}, Scale: 1, Null: config.NullFail},
			},
			{
				Name:    "app_jobs_oldest_age_seconds",
				Type:    config.MetricGauge,
				Mapping: config.Mapping{Value: "age", Labels: map[string]string{"status": "status"}, Scale: 1, Null: config.NullFail},
			},
		},
	})

	snap, err := g.Map(source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": int64(12), "age": 1.5},
	}}, collectedAt)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	if snap.CollectionID != "appdb-jobs" || snap.SourceID != "appdb" {
		t.Errorf("identity = %q/%q, want %q/%q", snap.CollectionID, snap.SourceID, "appdb-jobs", "appdb")
	}
	if !snap.CollectedAt.Equal(collectedAt) {
		t.Errorf("CollectedAt = %v, want %v", snap.CollectedAt, collectedAt)
	}
	if len(snap.Families) != 2 {
		t.Fatalf("families = %d, want 2", len(snap.Families))
	}
	if snap.SampleCount() != 2 {
		t.Errorf("SampleCount = %d, want 2", snap.SampleCount())
	}
	assertFamily(t, snap.Families[0], snapshot.Family{
		Name: "app_jobs_by_status",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "status", Value: "pending"}}, Value: 12},
		},
	})
	assertFamily(t, snap.Families[1], snapshot.Family{
		Name: "app_jobs_oldest_age_seconds",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "status", Value: "pending"}}, Value: 1.5},
		},
	})
}

// §8.2: one family's failure fails the whole publication, never half of it.
func TestGroupFailsAtomically(t *testing.T) {
	g := mustCompileGroup(t, mapping.GroupSpec{
		CollectionID: "appdb-jobs",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metrics: []config.Metric{
			{Name: "app_good", Type: config.MetricGauge, Mapping: config.Mapping{Value: "value", Scale: 1, Null: config.NullFail}},
			{Name: "app_bad", Type: config.MetricGauge, Mapping: config.Mapping{Value: "missing", Scale: 1, Null: config.NullFail}},
		},
	})

	snap, err := g.Map(source.ResultSet{Rows: []source.Row{{"value": int64(1)}}}, collectedAt)
	if err == nil {
		t.Fatal("Map succeeded, want failure")
	}
	if snap != nil {
		t.Errorf("snapshot = %+v, want nil", snap)
	}
	if got := mapping.ReasonOf(err); got != telemetry.ReasonMissingField {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonMissingField)
	}
}

// A metric override replaces the plan default rather than merging with it.
func TestGroupUsesMetricLimitOverride(t *testing.T) {
	tight := config.Limits{MaxSeriesPerMetric: 1, MaxLabelValueBytes: 1024}
	g := mustCompileGroup(t, mapping.GroupSpec{
		CollectionID: "appdb-jobs",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metrics: []config.Metric{{
			Name:    "app_jobs_by_status",
			Type:    config.MetricGauge,
			Limits:  &tight,
			Mapping: config.Mapping{Value: "value", Labels: map[string]string{"status": "status"}, Scale: 1, Null: config.NullFail},
		}},
	})

	_, err := g.Map(source.ResultSet{Rows: []source.Row{
		{"status": "a", "value": int64(1)},
		{"status": "b", "value": int64(1)},
	}}, collectedAt)
	if got := mapping.ReasonOf(err); got != telemetry.ReasonCardinality {
		t.Errorf("reason = %q, want %q (err=%v)", got, telemetry.ReasonCardinality, err)
	}
}

// Two families of the same name would collide in exposition, so compilation rejects it.
func TestCompileGroupRejectsDuplicateFamily(t *testing.T) {
	m := config.Metric{Name: "app_dup", Type: config.MetricGauge, Mapping: config.Mapping{Value: "value", Scale: 1, Null: config.NullFail}}
	_, err := mapping.CompileGroup(mapping.GroupSpec{
		CollectionID: "plan",
		SourceID:     "appdb",
		Metrics:      []config.Metric{m, m},
	})
	if err == nil {
		t.Fatal("CompileGroup accepted a duplicate family name")
	}
}

func mustCompileGroup(t *testing.T, spec mapping.GroupSpec) *mapping.Group {
	t.Helper()
	g, err := mapping.CompileGroup(spec)
	if err != nil {
		t.Fatalf("CompileGroup: %v", err)
	}
	return g
}
