package mapping_test

import (
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

// §12.1: the jobs-by-status result and the exposition it must produce.
func TestMapSQLGaugeByStatus(t *testing.T) {
	m := mapping.Spec{
		CollectionID: "plan-1",
		SourceID:     "appdb",
		Style:        mapping.StyleColumns,
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name: "app_jobs_by_status",
			Help: "Current job count by status",
			Type: config.MetricGauge,
			Mapping: config.Mapping{
				Value:  "value",
				Labels: map[string]string{"status": "status"},
				Scale:  1,
				Null:   config.NullFail,
			},
		},
	}

	mapper, err := mapping.Compile(m)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	rs := source.ResultSet{
		Columns: []source.Column{{Name: "status", Type: "text"}, {Name: "value", Type: "int8"}},
		Rows: []source.Row{
			{"status": "pending", "value": int64(12)},
			{"status": "running", "value": int64(4)},
			{"status": "failed", "value": int64(2)},
		},
	}

	got, err := mapper.Map(rs)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	want := snapshot.Family{
		Name: "app_jobs_by_status",
		Help: "Current job count by status",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "status", Value: "failed"}}, Value: 2},
			{Labels: []snapshot.Label{{Name: "status", Value: "pending"}}, Value: 12},
			{Labels: []snapshot.Label{{Name: "status", Value: "running"}}, Value: 4},
		},
	}
	assertFamily(t, got, want)
}

func assertFamily(t *testing.T, got, want snapshot.Family) {
	t.Helper()
	if got.Name != want.Name || got.Help != want.Help || got.Type != want.Type {
		t.Errorf("metadata = %q/%q/%q, want %q/%q/%q",
			got.Name, got.Help, got.Type, want.Name, want.Help, want.Type)
	}
	if len(got.Samples) != len(want.Samples) {
		t.Fatalf("samples = %v, want %v", got.Samples, want.Samples)
	}
	for i := range want.Samples {
		g, w := got.Samples[i], want.Samples[i]
		if g.Value != w.Value {
			t.Errorf("sample[%d] value = %v, want %v", i, g.Value, w.Value)
		}
		if len(g.Labels) != len(w.Labels) {
			t.Fatalf("sample[%d] labels = %v, want %v", i, g.Labels, w.Labels)
		}
		for j := range w.Labels {
			if g.Labels[j] != w.Labels[j] {
				t.Errorf("sample[%d] label[%d] = %v, want %v", i, j, g.Labels[j], w.Labels[j])
			}
		}
	}
}
