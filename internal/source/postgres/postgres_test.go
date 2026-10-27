package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

func validSettings() *source.PostgresSettings {
	return &source.PostgresSettings{
		DSN:          "postgres://metricule:s3cret@db.invalid:5432/app",
		MaxOpen:      4,
		MaxIdle:      2,
		QueryTimeout: 2 * time.Second,
	}
}

func TestNewReportsIdentity(t *testing.T) {
	s, err := New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: validSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.ID(); got != "appdb" {
		t.Errorf("ID() = %q, want %q", got, "appdb")
	}
	if got := s.Type(); got != source.TypePostgres {
		t.Errorf("Type() = %q, want %q", got, source.TypePostgres)
	}
}

// M3 implements querying, notifications, and opt-in trigger management (§19).
func TestCapabilitiesReportOnlyImplementedSupport(t *testing.T) {
	s, err := New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: validSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := source.Capabilities{Query: true, Poll: true, Notifications: true, TriggerManagement: true}
	if got := s.Capabilities(); got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestPostgresIsRegistered(t *testing.T) {
	if !source.Implemented(source.TypePostgres) {
		t.Fatal("postgres adapter is not registered")
	}
	s, err := source.New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: validSettings()})
	if err != nil {
		t.Fatalf("source.New: %v", err)
	}
	if s.Type() != source.TypePostgres {
		t.Errorf("registry built %q", s.Type())
	}
}

func TestNewRejectsBadSettings(t *testing.T) {
	cases := map[string]struct {
		raw  any
		want string
	}{
		"nil settings":     {nil, "settings"},
		"foreign settings": {&source.SQLiteSettings{Path: "x.db"}, "settings"},
		"empty dsn": {&source.PostgresSettings{
			MaxOpen: 1, QueryTimeout: time.Second,
		}, "dsn"},
		"unparsable dsn": {&source.PostgresSettings{
			DSN: "postgres://%zz", MaxOpen: 1, QueryTimeout: time.Second,
		}, "dsn"},
		"no connections": {&source.PostgresSettings{
			DSN: "postgres:///app", MaxOpen: 0, QueryTimeout: time.Second,
		}, "max_open"},
		"no timeout": {&source.PostgresSettings{
			DSN: "postgres:///app", MaxOpen: 1, QueryTimeout: 0,
		}, "query_timeout"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: tc.raw})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Error("error leaked a credential")
			}
		})
	}
}

// Construction must not dial: configuration compiles before any source is opened.
func TestNewDoesNotConnect(t *testing.T) {
	s, err := New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: validSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close before Open: %v", err)
	}
}

func TestQueryBeforeOpenFails(t *testing.T) {
	s, err := New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: validSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	compiled, err := s.CompileQuery(source.QueryDefinition{MetricName: "jobs", SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	_, err = s.Query(t.Context(), source.QueryRequest{Spec: compiled, MaxRows: 1})
	if !errors.Is(err, ErrNotOpen) {
		t.Errorf("Query before Open = %v, want ErrNotOpen", err)
	}
	if err := s.Health(t.Context()); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Health before Open = %v, want ErrNotOpen", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s, err := New(source.Settings{ID: "appdb", Type: source.TypePostgres, Raw: validSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := range 3 {
		if err := s.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
}
