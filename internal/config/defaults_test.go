package config

import (
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// Pins every default documented in docs/configuration.md.
//
// Expected values are literals taken from the §5.1 contract and the reference table,
// not from the Default* constants: reading the constants would assert nothing beyond
// "the code equals itself".
func TestDocumentedDefaults(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  pg:
    type: postgres
    dsn:
      env: APP_DATABASE_URL
    triggers:
      manage: true
  db:
    type: sqlite
    path: /data/app.db
  api:
    type: http
    base_url: http://app:8080
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
`

	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	t.Run("server", func(t *testing.T) {
		checkString(t, "listen", cfg.Server.Listen, "0.0.0.0:9108")
		checkString(t, "metrics_path", cfg.Server.MetricsPath, "/metrics")
		checkString(t, "health_path", cfg.Server.HealthPath, "/healthz")
		checkString(t, "readiness_path", cfg.Server.ReadinessPath, "/readyz")
	})

	t.Run("collection", func(t *testing.T) {
		c := cfg.Defaults.Collection
		checkDuration(t, "interval", c.Interval, 30*time.Second)
		checkDuration(t, "timeout", c.Timeout, 10*time.Second)
		checkDuration(t, "reconcile_interval", c.ReconcileInterval, 5*time.Minute)
		checkDuration(t, "debounce", c.Debounce, 250*time.Millisecond)
		checkDuration(t, "jitter", c.Jitter, 0)
	})

	t.Run("limits", func(t *testing.T) {
		l := cfg.Defaults.Limits
		checkInt(t, "max_series_per_metric", l.MaxSeriesPerMetric, 1000)
		checkInt(t, "max_rows_per_query", l.MaxRowsPerQuery, 10000)
		checkInt(t, "max_concurrent_collections", l.MaxConcurrentCollections, 4)
		checkInt(t, "max_total_series", l.MaxTotalSeries, 100000)
		checkInt(t, "max_metrics", l.MaxMetrics, 1000)
		checkInt(t, "max_label_value_bytes", l.MaxLabelValueBytes, 1024)
	})

	t.Run("failure", func(t *testing.T) {
		if got := cfg.Defaults.Failure.Mode; got != FailureRetain {
			t.Errorf("failure.mode = %q, want retain", got)
		}
		checkDuration(t, "max_stale_age", cfg.Defaults.Failure.MaxStaleAge, 5*time.Minute)
	})

	t.Run("postgres", func(t *testing.T) {
		p := cfg.Sources["pg"].Postgres
		checkInt(t, "pool.max_open", p.Pool.MaxOpen, 4)
		checkInt(t, "pool.max_idle", p.Pool.MaxIdle, 2)
		// Inherited from defaults.collection.timeout, not a separate constant.
		checkDuration(t, "query_timeout", p.QueryTimeout, 10*time.Second)
		checkString(t, "triggers.schema", p.Triggers.Schema, "metricule")
		if p.Triggers.CleanupOnShutdown {
			t.Error("triggers.cleanup_on_shutdown must default to false")
		}
	})

	t.Run("sqlite", func(t *testing.T) {
		s := cfg.Sources["db"].SQLite
		if !s.ReadOnly {
			t.Error("readonly must be true (AD-007)")
		}
		checkDuration(t, "busy_timeout", s.BusyTimeout, 3*time.Second)
		checkDuration(t, "change_check_interval", s.ChangeCheckInterval, 2*time.Second)
	})

	t.Run("http", func(t *testing.T) {
		h := cfg.Sources["api"].HTTP
		checkDuration(t, "timeout", h.Timeout, 5*time.Second)
		if got, want := h.MaxResponseBytes, int64(4194304); got != want {
			t.Errorf("max_response_bytes = %d, want %d", got, want)
		}
		checkInt(t, "max_redirects", h.MaxRedirects, 0)
		checkInt(t, "retries", h.Retries, 2)
	})

	t.Run("metric", func(t *testing.T) {
		m := cfg.Metrics[0]
		if m.Collect.Mode != ModePoll {
			t.Errorf("collect.mode = %q, want poll when the block is omitted", m.Collect.Mode)
		}
		checkDuration(t, "collect.interval", m.Collect.Interval, 30*time.Second)
		checkDuration(t, "collect.timeout", m.Collect.Timeout, 10*time.Second)
		checkDuration(t, "collect.debounce", m.Collect.Debounce, 250*time.Millisecond)
		checkDuration(t, "collect.jitter", m.Collect.Jitter, 0)
		// Reconciliation applies to change-driven modes only.
		checkDuration(t, "collect.fallback_interval", m.Collect.FallbackInterval, 0)
		if m.Mapping.Scale != 1 {
			t.Errorf("mapping.scale = %v, want 1", m.Mapping.Scale)
		}
		if m.Mapping.Null != NullFail {
			t.Errorf("mapping.null = %q, want fail: null must never become zero", m.Mapping.Null)
		}
		checkInt(t, "limits.max_series_per_metric", m.Limits.MaxSeriesPerMetric, 1000)
		if m.Failure.Mode != FailureRetain {
			t.Errorf("failure.mode = %q, want retain", m.Failure.Mode)
		}
		checkDuration(t, "failure.max_stale_age", m.Failure.MaxStaleAge, 5*time.Minute)
	})
}

// Change-driven modes inherit reconcile_interval as their fallback.
func TestFallbackIntervalInheritsReconcileInterval(t *testing.T) {
	doc := postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
      channel: app_changed
`)

	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	checkDuration(t, "collect.fallback_interval", cfg.Metrics[0].Collect.FallbackInterval, 5*time.Minute)
}

// Metric overrides inherit the remaining fields of the block they override.
func TestPartialOverridesInheritFieldByField(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  collection:
    interval: 1m
    timeout: 20s
  limits:
    max_series_per_metric: 500
    max_rows_per_query: 2000
  failure:
    mode: retain
    max_stale_age: 9m
sources:
  db:
    type: sqlite
    path: /data/app.db
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
      interval: 5s
    limits:
      max_series_per_metric: 10
    failure:
      mode: drop
`

	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := cfg.Metrics[0]
	checkDuration(t, "collect.interval", m.Collect.Interval, 5*time.Second)
	checkDuration(t, "collect.timeout", m.Collect.Timeout, 20*time.Second)
	checkInt(t, "limits.max_series_per_metric", m.Limits.MaxSeriesPerMetric, 10)
	checkInt(t, "limits.max_rows_per_query", m.Limits.MaxRowsPerQuery, 2000)
	if m.Failure.Mode != FailureDrop {
		t.Errorf("failure.mode = %q, want drop", m.Failure.Mode)
	}
	checkDuration(t, "failure.max_stale_age", m.Failure.MaxStaleAge, 9*time.Minute)
}

// Both spellings of the null-policy key decode, as documented.
func TestNullPolicyKeySpellings(t *testing.T) {
	for _, key := range []string{"null", `"null"`} {
		t.Run(key, func(t *testing.T) {
			doc := sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      ` + key + `: omit
    collect:
      mode: poll
      interval: 30s
`)
			cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := cfg.Metrics[0].Mapping.Null; got != NullOmit {
				t.Errorf("null policy = %q, want omit", got)
			}
		})
	}
}

// `validate --print-config` output is for humans, not for reloading.
//
// Secret references serialize to their reference form ("env:APP_PASSWORD"), which is a
// scalar where the loader expects a mapping. Pinning it stops someone documenting the
// dump as a round-trip format.
func TestRedactedConfigurationIsHumanFacingOnly(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
    authentication:
      type: basic
      username: metrics
      password:
        env: APP_PASSWORD
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.value"
      null: omit
    collect:
      mode: poll
      interval: 30s
`

	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := Redacted(cfg)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	if !strings.Contains(string(out), "env:APP_PASSWORD") {
		t.Errorf("dump lost the secret reference form:\n%s", out)
	}
	if _, _, err := Parse(out, source.CapabilitiesFor); err == nil {
		t.Error("redacted output must not be presented as a reloadable document")
	}
}

func checkString(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", field, got, want)
	}
}

func checkInt(t *testing.T, field string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", field, got, want)
	}
}

func checkDuration(t *testing.T, field string, got Duration, want time.Duration) {
	t.Helper()
	if got.Duration() != want {
		t.Errorf("%s = %s, want %s", field, got, want)
	}
}
