package sqlite

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

func testSettings(path string) source.Settings {
	return source.Settings{
		ID:   "app",
		Type: source.TypeSQLite,
		Raw: &source.SQLiteSettings{
			Path:                path,
			BusyTimeout:         3 * time.Second,
			ChangeCheckInterval: 2 * time.Second,
		},
	}
}

func TestRegisteredWithTheSharedRegistry(t *testing.T) {
	if !source.Implemented(source.TypeSQLite) {
		t.Fatal("sqlite adapter is not registered")
	}
	s, err := source.New(testSettings("/tmp/absent.db"))
	if err != nil {
		t.Fatalf("source.New: %v", err)
	}
	if s.ID() != "app" || s.Type() != source.TypeSQLite {
		t.Errorf("id/type = %q/%q, want app/sqlite", s.ID(), s.Type())
	}
}

// M1 implements querying only; change tokens arrive in M2 (§10.2).
func TestCapabilitiesReportOnlyImplementedSupport(t *testing.T) {
	s, err := New(testSettings("/tmp/absent.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := source.Capabilities{Query: true, Poll: true}
	if got := s.Capabilities(); got != want {
		t.Errorf("capabilities = %+v, want %+v", got, want)
	}
}

func TestNewRejectsForeignSettings(t *testing.T) {
	_, err := New(source.Settings{ID: "app", Type: source.TypeSQLite, Raw: &source.PostgresSettings{}})
	if err == nil {
		t.Fatal("New accepted postgres settings")
	}
}

func TestNewRejectsAnEmptyPath(t *testing.T) {
	_, err := New(testSettings(""))
	if err == nil {
		t.Fatal("New accepted an empty path")
	}
}

func TestNewRejectsANonPositiveBusyTimeout(t *testing.T) {
	_, err := New(source.Settings{
		ID:   "app",
		Type: source.TypeSQLite,
		Raw:  &source.SQLiteSettings{Path: "/tmp/a.db"},
	})
	if err == nil {
		t.Fatal("New accepted a zero busy timeout")
	}
}

func TestCompileQueryAcceptsSQL(t *testing.T) {
	s := mustNew(t, "/tmp/absent.db")

	spec, err := s.CompileQuery(source.QueryDefinition{MetricName: "users_total", SQL: "  SELECT count(*) AS n FROM users  "})
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	desc := spec.Describe()
	if !strings.Contains(desc, "users_total") || !strings.Contains(desc, "FROM users") {
		t.Errorf("Describe() = %q, want it to name the metric and the statement", desc)
	}
}

func TestCompileQueryRejectsMissingSQL(t *testing.T) {
	s := mustNew(t, "/tmp/absent.db")

	for _, def := range []source.QueryDefinition{
		{MetricName: "m"},
		{MetricName: "m", SQL: "   \n\t "},
	} {
		if _, err := s.CompileQuery(def); err == nil {
			t.Errorf("CompileQuery(%+v) accepted an empty statement", def)
		}
	}
}

// A misconfigured metric must fail at compile time, not at scrape time.
func TestCompileQueryRejectsHTTPFields(t *testing.T) {
	s := mustNew(t, "/tmp/absent.db")

	defs := []source.QueryDefinition{
		{MetricName: "m", SQL: "SELECT 1", Method: "GET"},
		{MetricName: "m", SQL: "SELECT 1", Path: "/status"},
		{MetricName: "m", SQL: "SELECT 1", Params: map[string]string{"a": "b"}},
		{MetricName: "m", SQL: "SELECT 1", Headers: map[string]string{"a": "b"}},
	}
	for _, def := range defs {
		if _, err := s.CompileQuery(def); err == nil {
			t.Errorf("CompileQuery(%+v) accepted an HTTP field", def)
		}
	}
}

func TestQueryRejectsAForeignSpec(t *testing.T) {
	s := mustNew(t, "/tmp/absent.db")

	_, err := s.Query(t.Context(), source.QueryRequest{Spec: foreignSpec{}, MaxRows: 10})
	if err == nil {
		t.Fatal("Query accepted a spec compiled by another adapter")
	}
	if errors.Is(err, errOpenRequired) {
		t.Fatalf("spec validation deferred to connection handling: %v", err)
	}
}

type foreignSpec struct{}

func (foreignSpec) Describe() string { return "not a sqlite spec" }

func mustNew(t *testing.T, path string) source.Source {
	t.Helper()
	s, err := New(testSettings(path))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}
