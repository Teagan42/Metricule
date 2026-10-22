package httpjson_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/teagan42/metricule/internal/source"
	_ "github.com/teagan42/metricule/internal/source/httpjson"
)

// Builds a source against a live test server. httptest speaks real HTTP, so
// transport, status, redirect, and timeout behavior is exercised rather than
// mocked (rule 9).
func newSource(t *testing.T, cfg source.HTTPSettings) source.Source {
	t.Helper()
	s, err := source.New(source.Settings{ID: "appapi", Type: source.TypeHTTP, Raw: &cfg})
	if err != nil {
		t.Fatalf("source.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// Serves one JSON document at /api/status.
func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func compile(t *testing.T, s source.Source, def source.QueryDefinition) source.QuerySpec {
	t.Helper()
	if def.Path == "" {
		def.Path = "/api/status"
	}
	if def.MetricName == "" {
		def.MetricName = "app_metric"
	}
	spec, err := s.CompileQuery(def)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	return spec
}

func query(t *testing.T, s source.Source, spec source.QuerySpec, maxRows int) source.ResultSet {
	t.Helper()
	rs, err := s.Query(context.Background(), source.QueryRequest{Spec: spec, MaxRows: maxRows})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return rs
}

func queryErr(t *testing.T, s source.Source, spec source.QuerySpec, maxRows int) error {
	t.Helper()
	rs, err := s.Query(context.Background(), source.QueryRequest{Spec: spec, MaxRows: maxRows})
	if err == nil {
		t.Fatalf("expected error, got %+v", rs)
	}
	return err
}
