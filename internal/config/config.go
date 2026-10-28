// Package config defines and validates Metricule's declarative configuration.
//
// The configuration format is a versioned public interface. Unknown keys fail
// validation rather than being silently ignored (§5). Per-source-type keys are
// validated against the declared source type, so a PostgreSQL source rejects SQLite
// fields.
package config

import (
	"github.com/teagan42/metricule/internal/secrets"
)

// APIVersion is the only configuration version supported by this build.
const APIVersion = "metricule.io/v1alpha1"

// Kind is the only supported document kind.
const Kind = "Exporter"

// Config is a parsed, defaulted, validated configuration document.
type Config struct {
	APIVersion string                  `yaml:"apiVersion"`
	Kind       string                  `yaml:"kind"`
	Metadata   Metadata                `yaml:"metadata"`
	Server     Server                  `yaml:"server"`
	Defaults   Defaults                `yaml:"defaults"`
	Sources    map[string]SourceConfig `yaml:"sources"`
	Metrics    []Metric                `yaml:"metrics"`

	// present records which paths the document stated, so defaulting can tell an
	// explicit zero from an absent field. Unexported, so it never serializes and
	// never appears in a redacted config dump.
	present fieldSet
}

// Metadata describes the exporter deployment. Informational only.
type Metadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
}

// Server configures the exposition endpoints.
type Server struct {
	Listen        string `yaml:"listen,omitempty"`
	MetricsPath   string `yaml:"metrics_path,omitempty"`
	HealthPath    string `yaml:"health_path,omitempty"`
	ReadinessPath string `yaml:"readiness_path,omitempty"`
}

// Defaults supplies inherited collection settings and resource bounds.
type Defaults struct {
	Collection CollectionDefaults `yaml:"collection,omitempty"`
	Limits     Limits             `yaml:"limits,omitempty"`
	Failure    Failure            `yaml:"failure,omitempty"`
}

// CollectionDefaults are inherited by metrics that omit the equivalent field.
type CollectionDefaults struct {
	Interval          Duration `yaml:"interval,omitempty"`
	Timeout           Duration `yaml:"timeout,omitempty"`
	ReconcileInterval Duration `yaml:"reconcile_interval,omitempty"`
	Debounce          Duration `yaml:"debounce,omitempty"`
	// Jitter spreads interval schedules to avoid synchronized query bursts (§7.3).
	Jitter Duration `yaml:"jitter,omitempty"`
}

// Limits bound resource consumption (P5, §8.7).
//
// MaxTotalSeries, MaxMetrics, and MaxLabelValueBytes are global safeguards required by
// §8.7 beyond the three fields shown in the §5.1 example.
type Limits struct {
	MaxSeriesPerMetric       int `yaml:"max_series_per_metric,omitempty"`
	MaxRowsPerQuery          int `yaml:"max_rows_per_query,omitempty"`
	MaxConcurrentCollections int `yaml:"max_concurrent_collections,omitempty"`
	MaxTotalSeries           int `yaml:"max_total_series,omitempty"`
	MaxMetrics               int `yaml:"max_metrics,omitempty"`
	MaxLabelValueBytes       int `yaml:"max_label_value_bytes,omitempty"`
}

// FailureMode selects what happens to published series after a failed collection.
type FailureMode string

const (
	// FailureRetain exposes the last successful snapshot until MaxStaleAge elapses.
	FailureRetain FailureMode = "retain"
	// FailureDrop omits affected samples immediately after a failure.
	FailureDrop FailureMode = "drop"
)

// Failure configures staleness handling (§8.4).
//
// Neither mode ever substitutes zero for an unavailable value (P3).
type Failure struct {
	Mode FailureMode `yaml:"mode,omitempty"`
	// MaxStaleAge bounds retention. After expiry the samples are omitted.
	MaxStaleAge Duration `yaml:"max_stale_age,omitempty"`
}

// MetricType is a Metricule output type, not necessarily a Prometheus wire type.
type MetricType string

const (
	// MetricGauge is a point-in-time value.
	MetricGauge MetricType = "gauge"
	// MetricCounter requires a persisted monotonic cumulative source value (§8.5).
	// A COUNT(*) of current rows is a gauge, not a counter.
	MetricCounter MetricType = "counter"
	// MetricStateSet emits one gauge series per configured state, 1 for the active
	// state and 0 for the rest. A mapping abstraction, not a wire type (§5.1).
	MetricStateSet MetricType = "state_set"
	// MetricHistogram exposes cumulative persisted buckets as a Prometheus
	// histogram. The source owns bucket arithmetic; mapping does not cumulate.
	MetricHistogram MetricType = "histogram"
)

// CollectMode selects the refresh mechanism (§7.1).
type CollectMode string

const (
	// ModePoll refreshes on a fixed interval.
	ModePoll CollectMode = "poll"
	// ModeNotify refreshes on source notifications plus mandatory reconciliation.
	ModeNotify CollectMode = "notify"
	// ModeAuto selects the best mechanism the adapter advertises.
	ModeAuto CollectMode = "auto"
	// ModeManual is reserved for a future admin interface. Rejected in v0.1.
	ModeManual CollectMode = "manual"
	// ModeCDC is reserved for durable event processing. Rejected in v0.1.
	ModeCDC CollectMode = "cdc"
)

// Metric declares one output metric family and how to collect it.
type Metric struct {
	Name   string     `yaml:"name"`
	Help   string     `yaml:"help"`
	Type   MetricType `yaml:"type"`
	Source string     `yaml:"source"`

	// Query is used by database sources. Mutually exclusive with Request.
	Query *Query `yaml:"query,omitempty"`
	// Request is used by HTTP sources. Mutually exclusive with Query.
	Request *Request `yaml:"request,omitempty"`

	Mapping Mapping `yaml:"mapping"`
	Collect Collect `yaml:"collect"`

	// Limits and Failure override the document defaults for this metric.
	Limits  *Limits  `yaml:"limits,omitempty"`
	Failure *Failure `yaml:"failure,omitempty"`
}

// Query is a SQL query definition.
type Query struct {
	SQL string `yaml:"sql"`
}

// Request is an HTTP request definition. GET only in v0.1 (§11.1).
type Request struct {
	Method  string            `yaml:"method,omitempty"`
	Path    string            `yaml:"path"`
	Params  map[string]string `yaml:"params,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// NullPolicy selects handling for null numeric values (§12.4).
type NullPolicy string

const (
	// NullFail fails the collection. The default; never coerces null to zero.
	NullFail NullPolicy = "fail"
	// NullOmit drops the affected sample and keeps the rest of the result.
	NullOmit NullPolicy = "omit"
)

// Mapping converts a normalized result into samples.
//
// Decoded by a custom unmarshaler because the §5.1 contract spells the null-policy key
// as `null`, which YAML resolves to the null scalar rather than the string "null".
type Mapping struct {
	// Rows selects repeating elements for multi-series results. Empty means the
	// result is treated as a single row.
	Rows string
	// Value selects the numeric sample: a column name for SQL, a selector for JSON.
	Value string
	// Buckets selects the cumulative bucket bound (`le`) for a histogram.
	Buckets string
	// Count selects the total observation count for a histogram. Every row for one
	// labelset must agree, and it must equal the +Inf bucket.
	Count string
	// Sum selects the total observation sum for a histogram.
	Sum string
	// Labels maps label name to a column name or selector.
	Labels map[string]string
	// StaticLabels are attached verbatim to every series.
	StaticLabels map[string]string
	// States enumerates the allowed values for a state_set metric. Bounded by
	// configuration, so state cardinality cannot grow from source data (§8.7).
	States []string
	// Scale multiplies the extracted value for unit conversion. Zero means 1.
	Scale float64
	// Null selects null handling. Defaults to NullFail.
	Null NullPolicy
}

// Collect configures scheduling for one metric.
type Collect struct {
	Mode CollectMode `yaml:"mode,omitempty"`
	// Interval applies to poll mode.
	Interval Duration `yaml:"interval,omitempty"`
	// Timeout bounds a single collection.
	Timeout Duration `yaml:"timeout,omitempty"`
	// Channel is the PostgreSQL NOTIFY channel for notify mode.
	Channel string `yaml:"channel,omitempty"`
	// FallbackInterval forces reconciliation for notify and auto modes. Mandatory
	// because notification delivery is not durable (AD-005).
	FallbackInterval Duration `yaml:"fallback_interval,omitempty"`
	// RequireNotifications fails startup when the source cannot deliver
	// notifications, instead of degrading to reconcile-only or change tokens.
	RequireNotifications bool     `yaml:"require_notifications,omitempty"`
	Debounce             Duration `yaml:"debounce,omitempty"`
	Jitter               Duration `yaml:"jitter,omitempty"`
	Watch                *Watch   `yaml:"watch,omitempty"`
}

// Watch narrows change observation to a scope.
type Watch struct {
	Tables []string `yaml:"tables,omitempty"`
	// Events filters DML kinds: insert, update, delete.
	Events []string `yaml:"events,omitempty"`
}

// SourceType identifies an adapter. AD-008 fixes the v0.1 set.
type SourceType string

// Supported source types.
const (
	SourcePostgres SourceType = "postgres"
	SourceSQLite   SourceType = "sqlite"
	SourceHTTP     SourceType = "http"
	SourceMySQL    SourceType = "mysql"
)

// SourceConfig is a discriminated union over adapter settings.
//
// Exactly one of Postgres, SQLite, HTTP, or MySQL is populated, matching Type.
type SourceConfig struct {
	Type SourceType

	Postgres *PostgresSource
	SQLite   *SQLiteSource
	HTTP     *HTTPSource
	MySQL    *MySQLSource
}

// PostgresSource configures the PostgreSQL adapter (§9).
type PostgresSource struct {
	DSN          secrets.Ref `yaml:"dsn"`
	Pool         Pool        `yaml:"pool,omitempty"`
	QueryTimeout Duration    `yaml:"query_timeout,omitempty"`
	TLS          *TLS        `yaml:"tls,omitempty"`
	// Triggers is opt-in trigger management (AD-006). Absent means disabled.
	Triggers *Triggers `yaml:"triggers,omitempty"`
}

// Pool bounds connection usage.
type Pool struct {
	MaxOpen int `yaml:"max_open,omitempty"`
	MaxIdle int `yaml:"max_idle,omitempty"`
}

// TLS configures transport security for database connections.
type TLS struct {
	// Mode maps to a libpq sslmode such as "require" or "verify-full".
	Mode     string `yaml:"mode,omitempty"`
	RootCert string `yaml:"root_cert,omitempty"`
	Cert     string `yaml:"cert,omitempty"`
	Key      string `yaml:"key,omitempty"`
}

// Triggers configures managed notification triggers. Disabled unless Manage is true.
type Triggers struct {
	Manage bool   `yaml:"manage"`
	Schema string `yaml:"schema,omitempty"`
	// CleanupOnShutdown defaults to false to avoid trigger churn on restart (§9.3).
	CleanupOnShutdown bool `yaml:"cleanup_on_shutdown,omitempty"`
	// DSN supplies separate provisioning credentials. Falls back to the query DSN,
	// which normally lacks the required privileges (§14.2).
	DSN secrets.Ref `yaml:"dsn,omitempty"`
}

// MySQLSource configures the MySQL adapter.
//
// TLS, timeouts, and character set are set through the DSN, which the driver
// parses natively. multiStatements is rejected by the adapter.
type MySQLSource struct {
	DSN          secrets.Ref `yaml:"dsn"`
	Pool         Pool        `yaml:"pool,omitempty"`
	QueryTimeout Duration    `yaml:"query_timeout,omitempty"`
}

// SQLiteSource configures the SQLite adapter (§10). Read-only in v0.1 (AD-007).
type SQLiteSource struct {
	Path string `yaml:"path"`
	// ReadOnly must be true in v0.1. Present so the config states the guarantee.
	ReadOnly bool `yaml:"readonly,omitempty"`
	// BusyTimeout bounds waiting on a locked database.
	BusyTimeout Duration `yaml:"busy_timeout,omitempty"`
	// ChangeCheckInterval paces PRAGMA data_version polling on the observer
	// connection.
	ChangeCheckInterval Duration `yaml:"change_check_interval,omitempty"`
}

// HTTPSource configures the HTTP/JSON adapter (§11).
type HTTPSource struct {
	BaseURL string            `yaml:"base_url"`
	Timeout Duration          `yaml:"timeout,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	// Authentication is optional. Credentials are secret references.
	Authentication *Authentication `yaml:"authentication,omitempty"`
	// AllowedPaths restricts request paths. Empty permits any path under BaseURL.
	AllowedPaths []string `yaml:"allowed_paths,omitempty"`
	// MaxResponseBytes bounds the read body (§11.3).
	MaxResponseBytes int64 `yaml:"max_response_bytes,omitempty"`
	// MaxRedirects bounds redirect following. Cross-origin redirects are refused
	// regardless of this value.
	MaxRedirects int `yaml:"max_redirects,omitempty"`
	// Retries bounds retry attempts per collection.
	Retries int `yaml:"retries,omitempty"`
}

// AuthType selects an HTTP authentication scheme.
type AuthType string

// Supported HTTP authentication schemes.
const (
	AuthBearer AuthType = "bearer"
	AuthBasic  AuthType = "basic"
)

// Authentication configures outbound HTTP credentials.
type Authentication struct {
	Type AuthType `yaml:"type"`
	// Token is required for bearer authentication.
	Token secrets.Ref `yaml:"token,omitempty"`
	// Username and Password are required for basic authentication.
	Username string      `yaml:"username,omitempty"`
	Password secrets.Ref `yaml:"password,omitempty"`
}
