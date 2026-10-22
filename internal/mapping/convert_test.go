package mapping_test

import (
	"encoding/json"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/source"
)

// §12.2: the supported value conversions.
func TestValueConversions(t *testing.T) {
	cases := map[string]struct {
		raw  any
		want float64
	}{
		"int":            {int(7), 7},
		"int32":          {int32(-3), -3},
		"int64":          {int64(1 << 40), 1 << 40},
		"uint64":         {uint64(9), 9},
		"float64":        {1.5, 1.5},
		"float32":        {float32(0.5), 0.5},
		"bool true":      {true, 1},
		"bool false":     {false, 0},
		"numeric string": {"12.25", 12.25},
		"numeric bytes":  {[]byte("42"), 42},
		"json number":    {json.Number("3.5"), 3.5},
		"exponent":       {"1e3", 1000},
	}

	mapper := mustCompile(t, scalarSpec(1))
	for name, tc := range cases {
		got, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"value": tc.raw}}})
		if err != nil {
			t.Errorf("%s: Map: %v", name, err)
			continue
		}
		if len(got.Samples) != 1 || got.Samples[0].Value != tc.want {
			t.Errorf("%s: samples = %v, want value %v", name, got.Samples, tc.want)
		}
	}
}

// §12.2: label values follow the same scalar conversions.
func TestLabelConversions(t *testing.T) {
	spec := scalarSpec(1)
	spec.Metric.Mapping.Labels = map[string]string{"kind": "kind"}
	mapper := mustCompile(t, spec)

	cases := map[string]struct {
		raw  any
		want string
	}{
		"string": {"queue", "queue"},
		"bytes":  {[]byte("queue"), "queue"},
		"bool":   {true, "true"},
		"int64":  {int64(12), "12"},
		"float":  {1.5, "1.5"},
	}
	for name, tc := range cases {
		got, err := mapper.Map(source.ResultSet{Rows: []source.Row{
			{"value": int64(1), "kind": tc.raw},
		}})
		if err != nil {
			t.Errorf("%s: Map: %v", name, err)
			continue
		}
		if got.Samples[0].Labels[0].Value != tc.want {
			t.Errorf("%s: label = %q, want %q", name, got.Samples[0].Labels[0].Value, tc.want)
		}
	}
}

// An unset Scale means 1. Multiplying by the zero value would publish a fabricated
// zero (AD-010).
func TestUnsetScaleLeavesValueUnchanged(t *testing.T) {
	mapper := mustCompile(t, scalarSpec(0))

	got, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"value": int64(12)}}})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if got.Samples[0].Value != 12 {
		t.Errorf("value = %v, want 12", got.Samples[0].Value)
	}
}

// §12.2: a non-numeric type has no explicit coercion and fails.
func TestUnsupportedValueTypeFails(t *testing.T) {
	mapper := mustCompile(t, scalarSpec(1))

	for _, raw := range []any{map[string]any{"a": 1}, []any{1}, struct{}{}} {
		if _, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"value": raw}}}); err == nil {
			t.Errorf("Map(%T) succeeded, want failure", raw)
		}
	}
}

func scalarSpec(scale float64) mapping.Spec {
	return mapping.Spec{
		CollectionID: "plan-1",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name:    "app_jobs_pending",
			Type:    config.MetricGauge,
			Mapping: config.Mapping{Value: "value", Scale: scale, Null: config.NullFail},
		},
	}
}
