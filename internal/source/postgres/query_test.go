package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

func testSource(t *testing.T) *Source {
	t.Helper()
	s, err := NewSource("appdb", *validSettings())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	return s
}

func TestCompileQueryAcceptsSQL(t *testing.T) {
	s := testSource(t)
	compiled, err := s.CompileQuery(source.QueryDefinition{
		MetricName: "app_jobs_pending",
		SQL:        "  SELECT count(*) AS value\n  FROM jobs  ",
	})
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	got := compiled.Describe()
	if !strings.Contains(got, "app_jobs_pending") {
		t.Errorf("Describe() = %q, want the metric name", got)
	}
	if !strings.Contains(got, "SELECT count(*) AS value FROM jobs") {
		t.Errorf("Describe() = %q, want normalized SQL", got)
	}
}

func TestDescribeTruncatesLongSQL(t *testing.T) {
	s := testSource(t)
	compiled, err := s.CompileQuery(source.QueryDefinition{
		MetricName: "wide",
		SQL:        "SELECT " + strings.Repeat("a", describeLimit*2),
	})
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if got := compiled.Describe(); !strings.HasSuffix(got, "...") {
		t.Errorf("Describe() = %q, want a truncation marker", got)
	}
}

func TestCompileQueryRejectsUnsupportedDefinitions(t *testing.T) {
	s := testSource(t)
	cases := map[string]struct {
		def  source.QueryDefinition
		want string
	}{
		"empty sql":    {source.QueryDefinition{MetricName: "m"}, "query.sql is required"},
		"blank sql":    {source.QueryDefinition{MetricName: "m", SQL: "   \n"}, "query.sql is required"},
		"http method":  {source.QueryDefinition{MetricName: "m", SQL: "SELECT 1", Method: "GET"}, "query.method"},
		"http path":    {source.QueryDefinition{MetricName: "m", SQL: "SELECT 1", Path: "/v1"}, "query.path"},
		"http params":  {source.QueryDefinition{MetricName: "m", SQL: "SELECT 1", Params: map[string]string{"a": "b"}}, "query.params"},
		"http headers": {source.QueryDefinition{MetricName: "m", SQL: "SELECT 1", Headers: map[string]string{"a": "b"}}, "query.headers"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.CompileQuery(tc.def)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), `metric "m"`) {
				t.Errorf("error %q does not name the metric", err)
			}
		})
	}
}

func TestQueryRejectsForeignSpec(t *testing.T) {
	s := testSource(t)
	_, err := s.Query(t.Context(), source.QueryRequest{Spec: otherSpec{}, MaxRows: 1})
	if err == nil || !strings.Contains(err.Error(), "not compiled by the postgres adapter") {
		t.Fatalf("Query with a foreign spec = %v", err)
	}
}

type otherSpec struct{}

func (otherSpec) Describe() string { return "other" }

func TestQueryRejectsUnboundedMaxRows(t *testing.T) {
	s := testSource(t)
	compiled, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	for _, maxRows := range []int{0, -1} {
		_, err := s.Query(t.Context(), source.QueryRequest{Spec: compiled, MaxRows: maxRows})
		if err == nil || !strings.Contains(err.Error(), "max_rows") {
			t.Errorf("Query with MaxRows=%d = %v, want a max_rows error", maxRows, err)
		}
	}
}

func TestErrorsCarryReasons(t *testing.T) {
	s := testSource(t)
	_, err := s.CompileQuery(source.QueryDefinition{MetricName: "m"})

	var classified *Error
	if !errors.As(err, &classified) {
		t.Fatalf("CompileQuery error %T does not unwrap to *Error", err)
	}
	if classified.Reason() != telemetry.ReasonQuery {
		t.Errorf("Reason() = %q, want %q", classified.Reason(), telemetry.ReasonQuery)
	}
	if classified.Op != "compile" {
		t.Errorf("Op = %q, want %q", classified.Op, "compile")
	}
}
