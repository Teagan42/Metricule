//go:build configgaps

// Failing tests for §5.5 rules that are specified but not enforced.
//
// Run with: go test -tags configgaps ./internal/config/
//
// Build-tagged so `make check` stays green while the gap is recorded as executable
// evidence instead of prose. Every test here asserts the behavior the specification
// requires, never the behavior the code currently has. Delete the tag, not the
// assertions, once ApplyDefaults can distinguish an explicit zero from an unset field.
//
// Root cause for all of them: defaults.go treats zero as "unset" (`setInt`,
// `setDuration`), so an operator cannot state zero and validation never sees it.
// Fixing it needs presence tracking in internal/config, which is Agent 0's contract.
package config

import (
	"testing"

	"github.com/teagan42/metricule/internal/source"
)

// §5.5 "Invalid cardinality limits". An explicit zero cap is invalid: validateLimits
// requires every limit to exceed zero. Defaulting rewrites it to 1000 first, so the
// check never runs and the operator's stated intent is discarded silently.
//
// Also breaks ADR 0007's direction: the schema's `minimum: 1` rejects this document
// while Go accepts it.
func TestExplicitZeroSeriesLimitIsRejected(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  limits:
    max_series_per_metric: 0
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge

	cfg, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatalf("max_series_per_metric: 0 must be rejected; got max_series_per_metric = %d",
			cfg.Defaults.Limits.MaxSeriesPerMetric)
	}
	requireDiagnostic(t, ds, "defaults.limits.max_series_per_metric", "must be greater than zero, got 0")
}

// Same gap at the metric level, where a per-metric override of 0 is also discarded.
func TestExplicitZeroMetricLimitIsRejected(t *testing.T) {
	doc := sqliteDoc(`  - name: app_thing
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
    limits:
      max_series_per_metric: 0
`)

	cfg, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatalf("metrics[0].limits.max_series_per_metric: 0 must be rejected; got %d",
			cfg.Metrics[0].Limits.MaxSeriesPerMetric)
	}
	requireDiagnostic(t, ds, "metrics[0].limits.max_series_per_metric", "must be greater than zero, got 0")
}

// §11.3 bounds response bytes. An explicit 0 means "read nothing", which no collection
// can satisfy, so it must be refused rather than widened to 4 MiB.
//
// Also breaks ADR 0007's direction: the schema's `minimum: 1` rejects this document.
func TestExplicitZeroMaxResponseBytesIsRejected(t *testing.T) {
	doc := httpDoc("    max_response_bytes: 0\n", httpGauge)

	cfg, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatalf("max_response_bytes: 0 must be rejected; got %d",
			cfg.Sources["api"].HTTP.MaxResponseBytes)
	}
	requireDiagnostic(t, ds, "sources.api.max_response_bytes", "must be greater than zero")
}

// AD-007 fixes SQLite access to read-only. A document that asks for write access
// states a guarantee Metricule will not honor, so it must be refused, not quietly
// rewritten (rule 3: do not silently narrow supported functionality).
//
// Also breaks ADR 0007's direction: the schema's `"readonly": {"const": true}` rejects
// this document while Go accepts it and forces readonly back to true.
func TestSQLiteReadonlyFalseIsRejected(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: sqlite
    path: /data/app.db
    readonly: false
metrics:
` + sqliteGauge

	_, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatal("readonly: false must be rejected: v0.1 cannot honor write access (AD-007)")
	}
	requireDiagnostic(t, ds, "sources.db.readonly", "read-only")
}

// §5.5 "Incorrect collection configuration". `retries: 0` is a legitimate request for
// no retries and is accepted by the schema (`minimum: 0`), but defaulting replaces it
// with 2. The operator cannot express a no-retry policy at all.
func TestExplicitZeroRetriesIsHonoured(t *testing.T) {
	doc := httpDoc("    retries: 0\n", httpGauge)

	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("retries: 0 is valid configuration: %v", err)
	}
	if got := cfg.Sources["api"].HTTP.Retries; got != 0 {
		t.Errorf("retries = %d, want 0: an explicit zero must not inherit the default", got)
	}
}

// Same gap for the PostgreSQL idle pool. `max_idle: 0` closes idle connections and is
// accepted by the schema (`minimum: 0`); defaulting replaces it with 2.
func TestExplicitZeroMaxIdleIsHonoured(t *testing.T) {
	doc := postgresDoc(`    pool:
      max_open: 4
      max_idle: 0
`, postgresGauge)

	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("max_idle: 0 is valid configuration: %v", err)
	}
	if got := cfg.Sources["pg"].Postgres.Pool.MaxIdle; got != 0 {
		t.Errorf("pool.max_idle = %d, want 0: an explicit zero must not inherit the default", got)
	}
}

// §5.5 "Incorrect collection configuration". validateCollect requires a positive poll
// interval, but defaulting rewrites 0s to 30s before the check, so the branch is
// unreachable through Parse and the operator silently gets a schedule they did not ask
// for.
func TestExplicitZeroPollIntervalIsRejected(t *testing.T) {
	doc := sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 0s
`)

	cfg, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatalf("interval: 0s must be rejected; got interval = %s", cfg.Metrics[0].Collect.Interval)
	}
	requireDiagnostic(t, ds, "metrics[0].collect.interval", "poll mode requires a positive interval")
}

// §8.4 requires retain mode to carry a bounded age limit. validateFailure enforces it,
// but defaulting rewrites 0s to 5m first, so the branch is unreachable through Parse.
func TestExplicitZeroMaxStaleAgeIsRejected(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  failure:
    mode: retain
    max_stale_age: 0s
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge

	cfg, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatalf("retain with max_stale_age: 0s must be rejected; got %s",
			cfg.Defaults.Failure.MaxStaleAge)
	}
	requireDiagnostic(t, ds, "defaults.failure.max_stale_age", "retain mode requires a positive max_stale_age")
}
