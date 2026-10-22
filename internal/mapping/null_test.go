package mapping_test

import (
	"math"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// §12.4, AD-010: a null numeric value fails the collection by default.
func TestNullValueFailsCollection(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullFail))

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"status": "pending", "value": nil}}})
	if err == nil {
		t.Fatal("Map succeeded, want failure on null value")
	}
	if got := mapping.ReasonOf(err); got != telemetry.ReasonNullValue {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonNullValue)
	}
}

// §12.4: omit drops only the affected sample.
func TestNullValueOmitDropsOnlyAffectedSample(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullOmit))

	got, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": nil},
		{"status": "running", "value": int64(4)},
	}})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	want := snapshot.Family{
		Name: "app_jobs_by_status",
		Type: snapshot.TypeGauge,
		Samples: []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "status", Value: "running"}}, Value: 4},
		},
	}
	assertFamily(t, got, want)
}

// §12.4: a null label is a missing dimension, not an empty one.
func TestNullLabelFollowsPolicy(t *testing.T) {
	row := source.Row{"status": nil, "value": int64(4)}

	if _, err := mustCompile(t, gaugeSpec(config.NullFail)).Map(source.ResultSet{Rows: []source.Row{row}}); err == nil {
		t.Error("Map succeeded with a null label, want failure")
	}

	got, err := mustCompile(t, gaugeSpec(config.NullOmit)).Map(source.ResultSet{Rows: []source.Row{row}})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(got.Samples) != 0 {
		t.Errorf("samples = %v, want none", got.Samples)
	}
}

// §12.3: a conversion error identifies the metric, source, and mapping field.
func TestConversionErrorIdentifiesMapping(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullFail))

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": "not-a-number"},
	}})
	if err == nil {
		t.Fatal("Map succeeded, want conversion failure")
	}
	if got := mapping.ReasonOf(err); got != telemetry.ReasonConversion {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonConversion)
	}
	for _, want := range []string{"app_jobs_by_status", "appdb", "value"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %q", err, want)
		}
	}
}

// §12.3: a conversion error never carries the offending record data.
func TestConversionErrorExcludesRecordData(t *testing.T) {
	const secret = "pg://user:hunter2@db/app"
	mapper := mustCompile(t, gaugeSpec(config.NullFail))

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": secret},
	}})
	if err == nil {
		t.Fatal("Map succeeded, want conversion failure")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks record data: %q", err)
	}
}

// §8.3: a missing column fails the collection rather than yielding zero.
func TestMissingColumnFails(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullFail))

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"status": "pending"}}})
	if got := mapping.ReasonOf(err); got != telemetry.ReasonMissingField {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonMissingField)
	}
}

// §11.3, §8.3: non-finite values have no valid exposition form.
func TestNonFiniteValueFails(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullFail))

	for _, raw := range []any{"NaN", "+Inf", math.Inf(1), math.NaN()} {
		if _, err := mapper.Map(source.ResultSet{Rows: []source.Row{
			{"status": "pending", "value": raw},
		}}); err == nil {
			t.Errorf("Map(%v) succeeded, want failure", raw)
		}
	}
}

func gaugeSpec(null config.NullPolicy) mapping.Spec {
	return mapping.Spec{
		CollectionID: "plan-1",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: 1000, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name: "app_jobs_by_status",
			Type: config.MetricGauge,
			Mapping: config.Mapping{
				Value:  "value",
				Labels: map[string]string{"status": "status"},
				Scale:  1,
				Null:   null,
			},
		},
	}
}

func mustCompile(t *testing.T, spec mapping.Spec) *mapping.Mapper {
	t.Helper()
	m, err := mapping.Compile(spec)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return m
}
