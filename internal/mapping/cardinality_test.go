package mapping_test

import (
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// §8.3: duplicate labelsets are rejected, never overwritten.
func TestDuplicateLabelsetRejected(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullFail))

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": int64(12)},
		{"status": "pending", "value": int64(13)},
	}})
	if err == nil {
		t.Fatal("Map succeeded with a duplicate labelset, want failure")
	}
	if got := mapping.ReasonOf(err); got != telemetry.ReasonDuplicateSeries {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonDuplicateSeries)
	}
}

// §8.7: exceeding the series cap fails the collection; truncation is prohibited.
func TestMaxSeriesPerMetricFailsAtomically(t *testing.T) {
	spec := gaugeSpec(config.NullFail)
	spec.Limits.MaxSeriesPerMetric = 2
	mapper := mustCompile(t, spec)

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "a", "value": int64(1)},
		{"status": "b", "value": int64(1)},
		{"status": "c", "value": int64(1)},
	}})
	if err == nil {
		t.Fatal("Map succeeded past the series cap, want failure")
	}
	if got := mapping.ReasonOf(err); got != telemetry.ReasonCardinality {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonCardinality)
	}
}

// §8.7: a state_set counts every expanded series against the cap.
func TestStateSetCountsExpandedSeries(t *testing.T) {
	spec := stateSetSpec()
	spec.Limits.MaxSeriesPerMetric = 3
	mapper := mustCompile(t, spec)

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{{"$.state": "running"}}})
	if got := mapping.ReasonOf(err); got != telemetry.ReasonCardinality {
		t.Errorf("reason = %q, want %q (err=%v)", got, telemetry.ReasonCardinality, err)
	}
}

// §8.7: label values are bounded in bytes, not characters.
func TestMaxLabelValueBytes(t *testing.T) {
	spec := gaugeSpec(config.NullFail)
	spec.Limits.MaxLabelValueBytes = 8
	mapper := mustCompile(t, spec)

	// Four runes, twelve bytes.
	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "日本語だ", "value": int64(1)},
	}})
	if got := mapping.ReasonOf(err); got != telemetry.ReasonCardinality {
		t.Fatalf("reason = %q, want %q (err=%v)", got, telemetry.ReasonCardinality, err)
	}
	if strings.Contains(err.Error(), "日本語だ") {
		t.Errorf("error leaks record data: %q", err)
	}

	spec.Limits.MaxLabelValueBytes = 12
	if _, err := mustCompile(t, spec).Map(source.ResultSet{Rows: []source.Row{
		{"status": "日本語だ", "value": int64(1)},
	}}); err != nil {
		t.Errorf("Map at the exact limit: %v", err)
	}
}

// §8.5: a counter requires cumulative semantics, so a negative sample is invalid.
func TestNegativeCounterRejected(t *testing.T) {
	spec := gaugeSpec(config.NullFail)
	spec.Metric.Type = config.MetricCounter
	spec.Metric.Name = "app_jobs_processed_total"
	mapper := mustCompile(t, spec)

	_, err := mapper.Map(source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": int64(-1)},
	}})
	if got := mapping.ReasonOf(err); got != telemetry.ReasonConversion {
		t.Errorf("reason = %q, want %q (err=%v)", got, telemetry.ReasonConversion, err)
	}
}
