package mysql

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

func validSettings() *source.MySQLSettings {
	return &source.MySQLSettings{
		DSN:          "metricule:s3cret@tcp(db.invalid:3306)/app?parseTime=false",
		MaxOpen:      4,
		MaxIdle:      2,
		QueryTimeout: 2 * time.Second,
	}
}

func TestRegisteredWithTheSharedRegistry(t *testing.T) {
	if !source.Implemented(source.TypeMySQL) {
		t.Fatal("mysql adapter is not registered")
	}
	s, err := New(source.Settings{ID: "appdb", Type: source.TypeMySQL, Raw: validSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.ID() != "appdb" || s.Type() != source.TypeMySQL {
		t.Errorf("id/type = %q/%q", s.ID(), s.Type())
	}
	want := source.Capabilities{Query: true, Poll: true}
	if got := s.Capabilities(); got != want {
		t.Errorf("capabilities = %+v, want %+v", got, want)
	}
	declared, ok := source.CapabilitiesFor(source.TypeMySQL)
	if !ok || declared != want {
		t.Errorf("declared capabilities = %+v, want %+v", declared, want)
	}
}

func TestNewRejectsUnsafeOrInvalidSettings(t *testing.T) {
	cases := map[string]func(*source.MySQLSettings){
		"empty dsn":        func(s *source.MySQLSettings) { s.DSN = "" },
		"bad dsn":          func(s *source.MySQLSettings) { s.DSN = "not a dsn" },
		"multi statements": func(s *source.MySQLSettings) { s.DSN += "&multiStatements=true" },
		"zero open":        func(s *source.MySQLSettings) { s.MaxOpen = 0 },
		"negative idle":    func(s *source.MySQLSettings) { s.MaxIdle = -1 },
		"idle above open":  func(s *source.MySQLSettings) { s.MaxIdle = 9 },
		"zero timeout":     func(s *source.MySQLSettings) { s.QueryTimeout = 0 },
	}
	for name, mutate := range cases {
		settings := validSettings()
		mutate(settings)
		if _, err := NewSource("appdb", *settings); err == nil {
			t.Errorf("%s: settings accepted", name)
		}
	}
}

// A DSN parse failure must not echo the password.
func TestNewDoesNotLeakTheDSN(t *testing.T) {
	settings := validSettings()
	settings.DSN = "metricule:s3cret@tcp("
	_, err := NewSource("appdb", *settings)
	if err == nil {
		t.Fatal("invalid DSN accepted")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the credential: %v", err)
	}
}

func TestTargetIsCredentialFree(t *testing.T) {
	s, err := NewSource("appdb", *validSettings())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if strings.Contains(s.target, "s3cret") || strings.Contains(s.target, "metricule") {
		t.Errorf("target = %q, want host and database only", s.target)
	}
	if s.target != "db.invalid:3306/app" {
		t.Errorf("target = %q, want db.invalid:3306/app", s.target)
	}
}

func TestCompileQuery(t *testing.T) {
	s, err := NewSource("appdb", *validSettings())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	spec, err := s.CompileQuery(source.QueryDefinition{MetricName: "jobs", SQL: "SELECT count(*) AS value FROM jobs"})
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if got := spec.Describe(); !strings.Contains(got, "mysql read-only query") {
		t.Errorf("Describe = %q", got)
	}

	if _, err := s.CompileQuery(source.QueryDefinition{MetricName: "jobs"}); err == nil {
		t.Error("empty SQL accepted")
	}
	if _, err := s.CompileQuery(source.QueryDefinition{
		MetricName: "jobs", SQL: "SELECT 1", Method: "GET", Path: "/x",
	}); err == nil {
		t.Error("HTTP fields accepted on a MySQL source")
	}
}

func TestBuildRowRejectsDuplicates(t *testing.T) {
	columns := []source.Column{{Name: "value"}, {Name: "value"}}
	if _, err := buildRow(columns, []any{int64(1), int64(2)}); err == nil {
		t.Fatal("duplicate columns accepted")
	}
	row, err := buildRow([]source.Column{{Name: "value"}}, []any{[]byte("42")})
	if err != nil {
		t.Fatalf("buildRow: %v", err)
	}
	if got := row["value"].([]byte); string(got) != "42" {
		t.Errorf("value = %q", got)
	}
}

func TestNormalizeValue(t *testing.T) {
	cases := map[string]struct {
		in   any
		want any
	}{
		"nil":     {nil, nil},
		"bytes":   {[]byte("x"), []byte("x")},
		"string":  {"x", "x"},
		"int64":   {int64(2), int64(2)},
		"int32":   {int32(2), int64(2)},
		"uint64":  {uint64(2), int64(2)},
		"float32": {float32(1.5), float64(1.5)},
	}
	for name, tc := range cases {
		if got := normalizeValue(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: normalizeValue = %#v, want %#v", name, got, tc.want)
		}
	}
}

func TestClassify(t *testing.T) {
	if got := classify(context.DeadlineExceeded); got != telemetry.ReasonTimeout {
		t.Errorf("deadline = %q", got)
	}
	if got := classify(ErrResultTooLarge); got != telemetry.ReasonResultTooLarge {
		t.Errorf("row limit = %q", got)
	}
	if got := classify(ErrReadOnly); got != telemetry.ReasonQuery {
		t.Errorf("read-only = %q", got)
	}
	if got := classify(ErrNotOpen); got != telemetry.ReasonUnavailable {
		t.Errorf("not open = %q", got)
	}
}
