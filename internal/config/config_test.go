package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/source"
)

// Loads the specification's reference configuration.
//
// M0 exit condition: the §5.1 example parses and validates.
func TestReferenceExampleParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "reference.yaml"))
	if err != nil {
		t.Fatalf("read reference example: %v", err)
	}

	cfg, ds, err := Parse(data, source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("reference example must validate, got: %v", err)
	}
	for _, d := range ds.Warnings() {
		t.Logf("warning: %s", d)
	}

	if got, want := len(cfg.Sources), 3; got != want {
		t.Errorf("sources = %d, want %d", got, want)
	}
	if got, want := len(cfg.Metrics), 5; got != want {
		t.Errorf("metrics = %d, want %d", got, want)
	}

	// Defaults must be resolved before validation so downstream agents never see
	// zero values.
	if cfg.Sources["appdb"].Postgres.Pool.MaxOpen != 4 {
		t.Errorf("postgres pool.max_open = %d, want 4", cfg.Sources["appdb"].Postgres.Pool.MaxOpen)
	}
	if !cfg.Sources["localdb"].SQLite.ReadOnly {
		t.Error("sqlite source must be read-only (AD-007)")
	}
	if cfg.Metrics[0].Collect.Timeout.Duration() != DefaultTimeout {
		t.Errorf("metric timeout = %s, want inherited %s", cfg.Metrics[0].Collect.Timeout, DefaultTimeout)
	}
	if cfg.Metrics[0].Mapping.Scale != 1 {
		t.Errorf("mapping scale = %v, want 1", cfg.Metrics[0].Mapping.Scale)
	}
	if cfg.Metrics[0].Mapping.Null != NullFail {
		t.Errorf("null policy = %q, want %q: null must never become zero", cfg.Metrics[0].Mapping.Null, NullFail)
	}
	if cfg.Metrics[0].Limits.MaxSeriesPerMetric != 1000 {
		t.Errorf("inherited max_series_per_metric = %d, want 1000", cfg.Metrics[0].Limits.MaxSeriesPerMetric)
	}
	if cfg.Metrics[0].Failure.Mode != FailureRetain {
		t.Errorf("failure mode = %q, want %q", cfg.Metrics[0].Failure.Mode, FailureRetain)
	}

	// A COUNT(*) gauge named _total is legitimate and must not be rejected (§8.5).
	if cfg.Metrics[2].Type != MetricGauge {
		t.Errorf("app_users_total type = %q, want gauge", cfg.Metrics[2].Type)
	}
}

// Confirms secrets never reach a serialized configuration dump.
func TestRedactedOmitsSecrets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "reference.yaml"))
	if err != nil {
		t.Fatalf("read reference example: %v", err)
	}
	t.Setenv("APP_DATABASE_URL", "postgres://user:hunter2@db:5432/app")

	cfg, _, err := Parse(data, source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := Redacted(cfg)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if strings.Contains(string(out), "hunter2") {
		t.Error("redacted config leaked a resolved secret")
	}
	if !strings.Contains(string(out), "env:APP_DATABASE_URL") {
		t.Errorf("redacted config lost the secret reference form:\n%s", out)
	}
}

func parseInvalid(t *testing.T, yaml string) Diagnostics {
	t.Helper()
	_, ds, err := Parse([]byte(yaml), source.CapabilitiesFor)
	if err == nil {
		t.Fatalf("expected configuration to be rejected")
	}
	return ds
}

// Asserts a diagnostic exists for path containing substr.
func requireDiagnostic(t *testing.T, ds Diagnostics, path, substr string) {
	t.Helper()
	for _, d := range ds {
		if d.Path == path && strings.Contains(d.Message, substr) {
			return
		}
	}
	t.Errorf("missing diagnostic at %q containing %q; got:\n%s", path, substr, ds)
}

const minimalHeader = `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: sqlite
    path: /data/app.db
`

func TestUnknownKeysRejected(t *testing.T) {
	_, _, err := Parse([]byte(minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
    typo_field: oops
`), source.CapabilitiesFor)
	if err == nil {
		t.Fatal("unknown key must fail validation (§5)")
	}
	if !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("error should name the unknown key, got: %v", err)
	}
}

// Per-source-type key checking: a sqlite source must reject postgres fields.
func TestSourceTypeRejectsForeignKeys(t *testing.T) {
	_, _, err := Parse([]byte(`apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: sqlite
    path: /data/app.db
    pool:
      max_open: 4
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`), source.CapabilitiesFor)
	if err == nil {
		t.Fatal("sqlite source must reject the postgres pool block")
	}
	if !strings.Contains(err.Error(), "pool") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestDuplicateMetricNames(t *testing.T) {
	ds := parseInvalid(t, minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
  - name: app_thing
    help: Another thing
    type: gauge
    source: db
    query:
      sql: SELECT 2 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[1].name", "duplicate metric name")
}

func TestMissingSourceReference(t *testing.T) {
	ds := parseInvalid(t, minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: nonexistent
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[0].source", "unknown source")
}

// SQLite advertises change tokens but not notifications, so notify mode must be
// refused with a pointer to auto mode (§6.2).
func TestNotifyModeRejectedForSQLite(t *testing.T) {
	ds := parseInvalid(t, minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
      channel: ch
      fallback_interval: 60s
`)
	requireDiagnostic(t, ds, "metrics[0].collect.mode", "does not support notifications")
}

func TestReservedTelemetryPrefix(t *testing.T) {
	ds := parseInvalid(t, minimalHeader+`
metrics:
  - name: metricule_custom_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[0].name", "reserved")
}

func TestReservedModesRejected(t *testing.T) {
	for _, mode := range []string{"manual", "cdc"} {
		t.Run(mode, func(t *testing.T) {
			ds := parseInvalid(t, minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: `+mode+`
`)
			requireDiagnostic(t, ds, "metrics[0].collect.mode", "not implemented in v0.1")
		})
	}
}

func TestStateSetRequiresStates(t *testing.T) {
	ds := parseInvalid(t, minimalHeader+`
metrics:
  - name: app_state
    help: State
    type: state_set
    source: db
    query:
      sql: SELECT 'running' AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[0].mapping.states", "require an explicit state list")
}

// The §5.1 contract spells the null policy as `null`, which YAML resolves to the null
// scalar rather than the string "null".
func TestNullPolicyKeyDecodes(t *testing.T) {
	cfg, _, err := Parse([]byte(minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      null: omit
    collect:
      mode: poll
      interval: 30s
`), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Metrics[0].Mapping.Null; got != NullOmit {
		t.Errorf("null policy = %q, want %q", got, NullOmit)
	}
}

func TestInvalidJSONSelectorRejected(t *testing.T) {
	ds := parseInvalid(t, `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.["
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[0].mapping.value", "selector")
}

// A query block against an HTTP source is configuration the adapter cannot execute.
func TestQueryBlockRejectedForHTTPSource(t *testing.T) {
	ds := parseInvalid(t, `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: api
    query:
      sql: SELECT 1 AS value
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[0].query", "does not accept a query block")
	requireDiagnostic(t, ds, "metrics[0].request", "requires a request block")
}

func TestAllowedPathsEnforced(t *testing.T) {
	ds := parseInvalid(t, `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
    allowed_paths:
      - /api/status
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /api/secrets
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`)
	requireDiagnostic(t, ds, "metrics[0].request.path", "allowed_paths")
}

func TestInvalidDurationReported(t *testing.T) {
	_, _, err := Parse([]byte(minimalHeader+`
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30 seconds
`), source.CapabilitiesFor)
	if err == nil {
		t.Fatal("invalid duration must fail")
	}
	if !strings.Contains(err.Error(), "invalid duration") {
		t.Errorf("error should explain the duration format, got: %v", err)
	}
}

// Diagnostic order must not depend on map iteration.
func TestDiagnosticsAreDeterministic(t *testing.T) {
	src := minimalHeader + `
metrics:
  - name: "bad name"
    help: ""
    type: nonsense
    source: missing
    mapping:
      value: ""
    collect:
      mode: nonsense
`
	var first string
	for i := 0; i < 20; i++ {
		_, ds, err := Parse([]byte(src), source.CapabilitiesFor)
		if err == nil {
			t.Fatal("expected rejection")
		}
		got := ds.String()
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("diagnostics differ between runs:\nrun 0:\n%s\nrun %d:\n%s", first, i, got)
		}
	}
}
