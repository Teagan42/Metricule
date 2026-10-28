package mapping_test

import (
	"math"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

func histogramSpec(scale float64, null config.NullPolicy, limit int) mapping.Spec {
	return mapping.Spec{
		CollectionID: "plan-1",
		SourceID:     "appdb",
		Limits:       config.Limits{MaxSeriesPerMetric: limit, MaxLabelValueBytes: 1024},
		Metric: config.Metric{
			Name: "request_duration_seconds",
			Help: "Request duration",
			Type: config.MetricHistogram,
			Mapping: config.Mapping{
				Value:   "value",
				Buckets: "le",
				Count:   "count",
				Sum:     "sum",
				Labels:  map[string]string{"route": "route"},
				Scale:   scale,
				Null:    null,
			},
		},
	}
}

func histogramRows() source.ResultSet {
	return source.ResultSet{
		Columns: []source.Column{
			{Name: "route", Type: "text"},
			{Name: "le", Type: "text"},
			{Name: "value", Type: "int8"},
			{Name: "count", Type: "int8"},
			{Name: "sum", Type: "float8"},
		},
		Rows: []source.Row{
			{"route": "api", "le": "0.1", "value": int64(1), "count": int64(5), "sum": 3.5},
			{"route": "api", "le": "1", "value": int64(3), "count": int64(5), "sum": 3.5},
			{"route": "api", "le": "+Inf", "value": int64(5), "count": int64(5), "sum": 3.5},
		},
	}
}

func TestHistogramBuildsCumulativeBuckets(t *testing.T) {
	mapper, err := mapping.Compile(histogramSpec(1, config.NullFail, 100))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	fam, err := mapper.Map(histogramRows())
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if fam.Type != snapshot.TypeHistogram || len(fam.Histograms) != 1 || len(fam.Samples) != 0 {
		t.Fatalf("family = %+v", fam)
	}
	h := fam.Histograms[0]
	if h.Count != 5 || h.Sum != 3.5 {
		t.Errorf("count/sum = %d/%v", h.Count, h.Sum)
	}
	if len(h.Buckets) != 3 || h.Buckets[0.1] != 1 || h.Buckets[1] != 3 {
		t.Errorf("buckets = %v", h.Buckets)
	}
	if h.Buckets[math.Inf(1)] != 5 {
		t.Errorf("+Inf bucket = %d", h.Buckets[math.Inf(1)])
	}
	if len(h.Labels) != 1 || h.Labels[0].Value != "api" {
		t.Errorf("labels = %v", h.Labels)
	}
}

func TestHistogramScaleAppliesToBoundsAndSumOnly(t *testing.T) {
	mapper, err := mapping.Compile(histogramSpec(1000, config.NullFail, 100))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	fam, err := mapper.Map(histogramRows())
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	h := fam.Histograms[0]
	if h.Buckets[100] != 1 || h.Buckets[1000] != 3 {
		t.Errorf("scaled buckets = %v", h.Buckets)
	}
	if h.Count != 5 {
		t.Errorf("count was scaled: %d", h.Count)
	}
	if h.Sum != 3500 {
		t.Errorf("sum = %v, want 3500", h.Sum)
	}
}

func TestHistogramRejectsInvalidShapes(t *testing.T) {
	cases := []struct {
		name string
		rows func(source.ResultSet) source.ResultSet
		want string
	}{
		{
			name: "missing +Inf",
			rows: func(rs source.ResultSet) source.ResultSet {
				rs.Rows = rs.Rows[:2]
				return rs
			},
			want: "+Inf",
		},
		{
			name: "count mismatch",
			rows: func(rs source.ResultSet) source.ResultSet {
				rs.Rows[2]["value"] = int64(6)
				return rs
			},
			want: "does not equal count",
		},
		{
			name: "non-cumulative",
			rows: func(rs source.ResultSet) source.ResultSet {
				rs.Rows[1]["value"] = int64(0)
				return rs
			},
			want: "cumulative",
		},
		{
			name: "inconsistent count",
			rows: func(rs source.ResultSet) source.ResultSet {
				rs.Rows[1]["count"] = int64(4)
				return rs
			},
			want: "not constant",
		},
		{
			name: "duplicate bound",
			rows: func(rs source.ResultSet) source.ResultSet {
				rs.Rows[1]["le"] = "0.1"
				return rs
			},
			want: "more than once",
		},
	}
	for _, tc := range cases {
		mapper, err := mapping.Compile(histogramSpec(1, config.NullFail, 100))
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		_, err = mapper.Map(tc.rows(histogramRows()))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestHistogramNullOmitDropsTheWholeSeries(t *testing.T) {
	mapper, err := mapping.Compile(histogramSpec(1, config.NullOmit, 100))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rs := histogramRows()
	rs.Rows[1]["sum"] = nil
	fam, err := mapper.Map(rs)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(fam.Histograms) != 0 {
		t.Errorf("histograms = %+v, want none", fam.Histograms)
	}
}

func TestHistogramSeriesLimitCountsGeneratedSeries(t *testing.T) {
	// Three buckets produce five series (+Inf is a bucket, plus _sum and _count).
	mapper, err := mapping.Compile(histogramSpec(1, config.NullFail, 4))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := mapper.Map(histogramRows()); err == nil {
		t.Fatal("histogram exceeded the series limit without an error")
	}
}

func TestHistogramSeparateLabelsets(t *testing.T) {
	mapper, err := mapping.Compile(histogramSpec(1, config.NullFail, 100))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rs := histogramRows()
	for _, le := range []string{"0.1", "1", "+Inf"} {
		rs.Rows = append(rs.Rows, source.Row{
			"route": "web", "le": le, "value": int64(2), "count": int64(2), "sum": 0.5,
		})
	}
	fam, err := mapper.Map(rs)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(fam.Histograms) != 2 {
		t.Fatalf("histograms = %d, want 2", len(fam.Histograms))
	}
	if fam.Histograms[0].Labels[0].Value != "api" || fam.Histograms[1].Labels[0].Value != "web" {
		t.Errorf("order = %+v", fam.Histograms)
	}
}

func TestHistogramCompileRequirements(t *testing.T) {
	spec := histogramSpec(1, config.NullFail, 100)
	spec.Metric.Mapping.Buckets = ""
	if _, err := mapping.Compile(spec); err == nil {
		t.Error("histogram without buckets compiled")
	}

	gauge := mapping.Spec{
		CollectionID: "plan-1", SourceID: "appdb",
		Metric: config.Metric{
			Name: "g", Help: "g", Type: config.MetricGauge,
			Mapping: config.Mapping{Value: "v", Buckets: "le"},
		},
	}
	if _, err := mapping.Compile(gauge); err == nil {
		t.Error("gauge with histogram fields compiled")
	}
}
