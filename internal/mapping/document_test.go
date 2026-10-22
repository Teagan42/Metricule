package mapping_test

import (
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// §11.2 array example: a document adapter resolves selectors and keys the columns by
// selector text, so mapping needs no source-specific branch (ADR 0008).
func TestMapSelectorKeyedResultSet(t *testing.T) {
	mapper := mustCompile(t, mapping.Spec{
		CollectionID: "plan-2",
		SourceID:     "appapi",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name: "app_worker_active_tasks",
			Help: "Active tasks per worker",
			Type: config.MetricGauge,
			Mapping: config.Mapping{
				Rows:   "$.workers[*]",
				Value:  "$.active_tasks",
				Labels: map[string]string{"worker": "$.name"},
				Scale:  1,
				Null:   config.NullFail,
			},
		},
	})

	rs := source.ResultSet{
		Columns: []source.Column{
			{Name: "$.active_tasks", Type: "number"},
			{Name: "$.name", Type: "string"},
		},
		Rows: []source.Row{
			{"$.active_tasks": 7.0, "$.name": "worker-a"},
			{"$.active_tasks": 0.0, "$.name": "worker-b"},
		},
	}

	got, err := mapper.Map(rs)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	assertFamily(t, got, snapshot.Family{
		Name: "app_worker_active_tasks",
		Help: "Active tasks per worker",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "worker", Value: "worker-a"}}, Value: 7},
			{Labels: []snapshot.Label{{Name: "worker", Value: "worker-b"}}, Value: 0},
		},
	})
}

// §12.2: static labels attach verbatim and Scale converts units.
func TestStaticLabelsAndScale(t *testing.T) {
	mapper := mustCompile(t, mapping.Spec{
		SourceID: "appdb",
		Limits:   config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name: "app_cache_size_bytes",
			Type: config.MetricGauge,
			Mapping: config.Mapping{
				Value:        "kib",
				StaticLabels: map[string]string{"tier": "hot", "region": "us-east-1"},
				Scale:        1024,
				Null:         config.NullFail,
			},
		},
	})

	got, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"kib": int64(4)}}})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	assertFamily(t, got, snapshot.Family{
		Name: "app_cache_size_bytes",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{{
			Labels: []snapshot.Label{
				{Name: "region", Value: "us-east-1"},
				{Name: "tier", Value: "hot"},
			},
			Value: 4096,
		}},
	})
}

// §5.1, §8.5: state_set lowers to one gauge series per configured state.
func TestStateSetLowersToGauges(t *testing.T) {
	mapper := mustCompile(t, stateSetSpec())

	got, err := mapper.Map(source.ResultSet{
		Columns: []source.Column{{Name: "$.state", Type: "string"}},
		Rows:    []source.Row{{"$.state": "degraded"}},
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	assertFamily(t, got, snapshot.Family{
		Name: "app_service_state",
		Help: "Current service operating state",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "state", Value: "degraded"}}, Value: 1},
			{Labels: []snapshot.Label{{Name: "state", Value: "running"}}, Value: 0},
			{Labels: []snapshot.Label{{Name: "state", Value: "starting"}}, Value: 0},
			{Labels: []snapshot.Label{{Name: "state", Value: "stopped"}}, Value: 0},
		},
	})
}

// §8.7: state cardinality comes from configuration, so an unlisted state is rejected
// rather than published.
func TestStateSetRejectsUnconfiguredState(t *testing.T) {
	mapper := mustCompile(t, stateSetSpec())

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"$.state": "on_fire"}}})
	if err == nil {
		t.Fatal("Map succeeded with an unconfigured state, want failure")
	}
	if got := mapping.ReasonOf(err); got != telemetry.ReasonConversion {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonConversion)
	}
}

// §12.3: the rejected state value is source data and must not reach the error text.
func TestStateSetErrorExcludesStateValue(t *testing.T) {
	mapper := mustCompile(t, stateSetSpec())

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"$.state": "tenant-42-secret"}}})
	if err == nil {
		t.Fatal("Map succeeded, want failure")
	}
	if strings.Contains(err.Error(), "tenant-42-secret") {
		t.Errorf("error leaks record data: %q", err)
	}
}

// A multi-row state_set keeps its row labels alongside the generated state label.
func TestStateSetKeepsRowLabels(t *testing.T) {
	spec := stateSetSpec()
	spec.Metric.Mapping.Labels = map[string]string{"service": "name"}
	spec.Metric.Mapping.Value = "state"
	spec.Metric.Mapping.States = []string{"up", "down"}
	mapper := mustCompile(t, spec)

	got, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"name": "api", "state": "up"},
		{"name": "worker", "state": "down"},
	}})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	assertFamily(t, got, snapshot.Family{
		Name: "app_service_state",
		Help: "Current service operating state",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "service", Value: "api"}, {Name: "state", Value: "down"}}, Value: 0},
			{Labels: []snapshot.Label{{Name: "service", Value: "api"}, {Name: "state", Value: "up"}}, Value: 1},
			{Labels: []snapshot.Label{{Name: "service", Value: "worker"}, {Name: "state", Value: "down"}}, Value: 1},
			{Labels: []snapshot.Label{{Name: "service", Value: "worker"}, {Name: "state", Value: "up"}}, Value: 0},
		},
	})
}

func stateSetSpec() mapping.Spec {
	return mapping.Spec{
		CollectionID: "plan-3",
		SourceID:     "appapi",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name: "app_service_state",
			Help: "Current service operating state",
			Type: config.MetricStateSet,
			Mapping: config.Mapping{
				Value:  "$.state",
				States: []string{"starting", "running", "degraded", "stopped"},
				Scale:  1,
				Null:   config.NullFail,
			},
		},
	}
}

