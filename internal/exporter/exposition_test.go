package exporter_test

import (
	"net/http"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/source"
)

// §12.1 end to end: the specification's result set, configuration, and exposition.
//
// Mapping and exposition are tested together here because the §12.1 contract spans
// both: a change in either that breaks the documented output fails this test.
func TestSpecExampleMapsToSpecExposition(t *testing.T) {
	group, err := mapping.CompileGroup(mapping.GroupSpec{
		CollectionID: "appdb-jobs",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metrics: []config.Metric{{
			Name: "app_jobs_by_status",
			Help: "Current job count by status",
			Type: config.MetricGauge,
			Mapping: config.Mapping{
				Value:  "value",
				Labels: map[string]string{"status": "status"},
				Scale:  1,
				Null:   config.NullFail,
			},
		}},
	})
	if err != nil {
		t.Fatalf("CompileGroup: %v", err)
	}

	snap, err := group.Map(source.ResultSet{
		Columns: []source.Column{{Name: "status", Type: "text"}, {Name: "value", Type: "int8"}},
		Rows: []source.Row{
			{"status": "pending", "value": int64(12)},
			{"status": "running", "value": int64(4)},
			{"status": "failed", "value": int64(2)},
		},
	}, scrapeTime)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	store := newFakeStore(snap)
	body, code := get(t, newHandler(t, store, nil), "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body != wantJobsExposition {
		t.Errorf("exposition =\n%s\nwant\n%s", body, wantJobsExposition)
	}
}

// §5.1: the state_set example publishes one gauge series per configured state.
func TestStateSetExampleExposition(t *testing.T) {
	group, err := mapping.CompileGroup(mapping.GroupSpec{
		CollectionID: "appapi-state",
		SourceID:     "appapi",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metrics: []config.Metric{{
			Name: "app_service_state",
			Help: "Current service operating state",
			Type: config.MetricStateSet,
			Mapping: config.Mapping{
				Value:  "$.state",
				States: []string{"starting", "running", "degraded", "stopped"},
				Scale:  1,
				Null:   config.NullFail,
			},
		}},
	})
	if err != nil {
		t.Fatalf("CompileGroup: %v", err)
	}

	snap, err := group.Map(source.ResultSet{
		Columns: []source.Column{{Name: "$.state", Type: "string"}},
		Rows:    []source.Row{{"$.state": "running"}},
	}, scrapeTime)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	want := `# HELP app_service_state Current service operating state
# TYPE app_service_state gauge
app_service_state{state="degraded"} 0
app_service_state{state="running"} 1
app_service_state{state="starting"} 0
app_service_state{state="stopped"} 0
`
	body, _ := get(t, newHandler(t, newFakeStore(snap), nil), "/metrics")
	if body != want {
		t.Errorf("exposition =\n%s\nwant\n%s", body, want)
	}
}
