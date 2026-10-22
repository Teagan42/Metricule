package config

import "time"

// Document defaults. Values match the §5.1 contract example where it states one.
const (
	DefaultListen        = "0.0.0.0:9108"
	DefaultMetricsPath   = "/metrics"
	DefaultHealthPath    = "/healthz"
	DefaultReadinessPath = "/readyz"

	DefaultInterval          = 30 * time.Second
	DefaultTimeout           = 10 * time.Second
	DefaultReconcileInterval = 5 * time.Minute
	DefaultDebounce          = 250 * time.Millisecond

	DefaultMaxSeriesPerMetric       = 1000
	DefaultMaxRowsPerQuery          = 10000
	DefaultMaxConcurrentCollections = 4
	DefaultMaxTotalSeries           = 100000
	DefaultMaxMetrics               = 1000
	DefaultMaxLabelValueBytes       = 1024

	DefaultMaxStaleAge = 5 * time.Minute

	DefaultPoolMaxOpen = 4
	DefaultPoolMaxIdle = 2

	DefaultBusyTimeout         = 3 * time.Second
	DefaultChangeCheckInterval = 2 * time.Second

	DefaultHTTPTimeout      = 5 * time.Second
	DefaultMaxResponseBytes = 4 << 20
	DefaultHTTPRetries      = 2
	// Redirects are not followed by default. A redirect can move a credentialed
	// request to another origin (§11.3).
	DefaultMaxRedirects = 0

	DefaultTriggerSchema = "metricule"
)

// ApplyDefaults fills unset fields from document defaults and built-in constants.
//
// Runs before validation so that validation sees effective values.
func ApplyDefaults(c *Config) {
	setString(&c.Server.Listen, DefaultListen)
	setString(&c.Server.MetricsPath, DefaultMetricsPath)
	setString(&c.Server.HealthPath, DefaultHealthPath)
	setString(&c.Server.ReadinessPath, DefaultReadinessPath)

	setDuration(&c.Defaults.Collection.Interval, DefaultInterval)
	setDuration(&c.Defaults.Collection.Timeout, DefaultTimeout)
	setDuration(&c.Defaults.Collection.ReconcileInterval, DefaultReconcileInterval)
	setDuration(&c.Defaults.Collection.Debounce, DefaultDebounce)

	setInt(&c.Defaults.Limits.MaxSeriesPerMetric, DefaultMaxSeriesPerMetric)
	setInt(&c.Defaults.Limits.MaxRowsPerQuery, DefaultMaxRowsPerQuery)
	setInt(&c.Defaults.Limits.MaxConcurrentCollections, DefaultMaxConcurrentCollections)
	setInt(&c.Defaults.Limits.MaxTotalSeries, DefaultMaxTotalSeries)
	setInt(&c.Defaults.Limits.MaxMetrics, DefaultMaxMetrics)
	setInt(&c.Defaults.Limits.MaxLabelValueBytes, DefaultMaxLabelValueBytes)

	if c.Defaults.Failure.Mode == "" {
		c.Defaults.Failure.Mode = FailureRetain
	}
	setDuration(&c.Defaults.Failure.MaxStaleAge, DefaultMaxStaleAge)

	for name, s := range c.Sources {
		applySourceDefaults(&s, c.Defaults)
		c.Sources[name] = s
	}

	for i := range c.Metrics {
		applyMetricDefaults(&c.Metrics[i], c.Defaults)
	}
}

func applySourceDefaults(s *SourceConfig, d Defaults) {
	switch {
	case s.Postgres != nil:
		setInt(&s.Postgres.Pool.MaxOpen, DefaultPoolMaxOpen)
		setInt(&s.Postgres.Pool.MaxIdle, DefaultPoolMaxIdle)
		setDuration(&s.Postgres.QueryTimeout, d.Collection.Timeout.Duration())
		if s.Postgres.Triggers != nil {
			setString(&s.Postgres.Triggers.Schema, DefaultTriggerSchema)
		}
	case s.SQLite != nil:
		// AD-007 fixes SQLite access to read-only, so the field cannot default to
		// false and silently grant write access.
		s.SQLite.ReadOnly = true
		setDuration(&s.SQLite.BusyTimeout, DefaultBusyTimeout)
		setDuration(&s.SQLite.ChangeCheckInterval, DefaultChangeCheckInterval)
	case s.HTTP != nil:
		setDuration(&s.HTTP.Timeout, DefaultHTTPTimeout)
		if s.HTTP.MaxResponseBytes == 0 {
			s.HTTP.MaxResponseBytes = DefaultMaxResponseBytes
		}
		if s.HTTP.Retries == 0 {
			s.HTTP.Retries = DefaultHTTPRetries
		}
	}
}

func applyMetricDefaults(m *Metric, d Defaults) {
	if m.Collect.Mode == "" {
		m.Collect.Mode = ModePoll
	}
	setDuration(&m.Collect.Interval, d.Collection.Interval.Duration())
	setDuration(&m.Collect.Timeout, d.Collection.Timeout.Duration())
	setDuration(&m.Collect.Debounce, d.Collection.Debounce.Duration())
	setDuration(&m.Collect.Jitter, d.Collection.Jitter.Duration())

	// Reconciliation is mandatory for change-driven modes because notification
	// delivery is not durable (AD-005).
	if m.Collect.Mode == ModeNotify || m.Collect.Mode == ModeAuto {
		setDuration(&m.Collect.FallbackInterval, d.Collection.ReconcileInterval.Duration())
	}

	if m.Mapping.Scale == 0 {
		m.Mapping.Scale = 1
	}
	if m.Mapping.Null == "" {
		m.Mapping.Null = NullFail
	}

	if m.Limits == nil {
		l := d.Limits
		m.Limits = &l
	} else {
		setInt(&m.Limits.MaxSeriesPerMetric, d.Limits.MaxSeriesPerMetric)
		setInt(&m.Limits.MaxRowsPerQuery, d.Limits.MaxRowsPerQuery)
		setInt(&m.Limits.MaxConcurrentCollections, d.Limits.MaxConcurrentCollections)
		setInt(&m.Limits.MaxTotalSeries, d.Limits.MaxTotalSeries)
		setInt(&m.Limits.MaxMetrics, d.Limits.MaxMetrics)
		setInt(&m.Limits.MaxLabelValueBytes, d.Limits.MaxLabelValueBytes)
	}

	if m.Failure == nil {
		f := d.Failure
		m.Failure = &f
	} else {
		if m.Failure.Mode == "" {
			m.Failure.Mode = d.Failure.Mode
		}
		setDuration(&m.Failure.MaxStaleAge, d.Failure.MaxStaleAge.Duration())
	}

	if m.Request != nil && m.Request.Method == "" {
		m.Request.Method = "GET"
	}
}

func setString(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

func setInt(dst *int, v int) {
	if *dst == 0 {
		*dst = v
	}
}

func setDuration(dst *Duration, v time.Duration) {
	if *dst == 0 {
		*dst = Duration(v)
	}
}
