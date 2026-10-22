package config

import (
	"fmt"
	"net/http"
	"time"
)

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
//
// A field the document stated is never overwritten, even when its value is zero.
// Zero is legitimate for some fields and invalid for others, and conflating it with
// "unset" both discards an operator's intent and hides invalid values from
// validation (§5.1). Presence comes from the parsed document; a hand-built Config
// has no presence information and so receives defaults for every zero field.
func ApplyDefaults(c *Config) {
	d := defaulter{c.present}

	d.str(&c.Server.Listen, "server.listen", DefaultListen)
	d.str(&c.Server.MetricsPath, "server.metrics_path", DefaultMetricsPath)
	d.str(&c.Server.HealthPath, "server.health_path", DefaultHealthPath)
	d.str(&c.Server.ReadinessPath, "server.readiness_path", DefaultReadinessPath)

	const dc = "defaults.collection"
	d.dur(&c.Defaults.Collection.Interval, dc+".interval", DefaultInterval)
	d.dur(&c.Defaults.Collection.Timeout, dc+".timeout", DefaultTimeout)
	d.dur(&c.Defaults.Collection.ReconcileInterval, dc+".reconcile_interval", DefaultReconcileInterval)
	d.dur(&c.Defaults.Collection.Debounce, dc+".debounce", DefaultDebounce)

	d.limits(&c.Defaults.Limits, "defaults.limits", Limits{
		MaxSeriesPerMetric:       DefaultMaxSeriesPerMetric,
		MaxRowsPerQuery:          DefaultMaxRowsPerQuery,
		MaxConcurrentCollections: DefaultMaxConcurrentCollections,
		MaxTotalSeries:           DefaultMaxTotalSeries,
		MaxMetrics:               DefaultMaxMetrics,
		MaxLabelValueBytes:       DefaultMaxLabelValueBytes,
	})

	if c.Defaults.Failure.Mode == "" && !d.set.has("defaults.failure.mode") {
		c.Defaults.Failure.Mode = FailureRetain
	}
	d.dur(&c.Defaults.Failure.MaxStaleAge, "defaults.failure.max_stale_age", DefaultMaxStaleAge)

	for name, s := range c.Sources {
		d.source(&s, fmt.Sprintf("sources.%s", name), c.Defaults)
		c.Sources[name] = s
	}

	for i := range c.Metrics {
		d.metric(&c.Metrics[i], fmt.Sprintf("metrics[%d]", i), c.Defaults)
	}
}

// Applies defaults only to fields the document left unset.
type defaulter struct{ set fieldSet }

func (d defaulter) str(dst *string, path, v string) {
	if !d.set.has(path) && *dst == "" {
		*dst = v
	}
}

func (d defaulter) num(dst *int, path string, v int) {
	if !d.set.has(path) && *dst == 0 {
		*dst = v
	}
}

func (d defaulter) num64(dst *int64, path string, v int64) {
	if !d.set.has(path) && *dst == 0 {
		*dst = v
	}
}

func (d defaulter) dur(dst *Duration, path string, v time.Duration) {
	if !d.set.has(path) && *dst == 0 {
		*dst = Duration(v)
	}
}

func (d defaulter) limits(dst *Limits, path string, v Limits) {
	d.num(&dst.MaxSeriesPerMetric, path+".max_series_per_metric", v.MaxSeriesPerMetric)
	d.num(&dst.MaxRowsPerQuery, path+".max_rows_per_query", v.MaxRowsPerQuery)
	d.num(&dst.MaxConcurrentCollections, path+".max_concurrent_collections", v.MaxConcurrentCollections)
	d.num(&dst.MaxTotalSeries, path+".max_total_series", v.MaxTotalSeries)
	d.num(&dst.MaxMetrics, path+".max_metrics", v.MaxMetrics)
	d.num(&dst.MaxLabelValueBytes, path+".max_label_value_bytes", v.MaxLabelValueBytes)
}

func (d defaulter) source(s *SourceConfig, path string, defs Defaults) {
	switch {
	case s.Postgres != nil:
		p := s.Postgres
		d.num(&p.Pool.MaxOpen, path+".pool.max_open", DefaultPoolMaxOpen)
		d.num(&p.Pool.MaxIdle, path+".pool.max_idle", DefaultPoolMaxIdle)
		d.dur(&p.QueryTimeout, path+".query_timeout", defs.Collection.Timeout.Duration())
		if p.Triggers != nil {
			d.str(&p.Triggers.Schema, path+".triggers.schema", DefaultTriggerSchema)
		}
	case s.SQLite != nil:
		p := s.SQLite
		// AD-007 fixes SQLite access to read-only. An omitted `readonly` means
		// read-only; an explicit `readonly: false` is rejected by validation
		// rather than silently flipped, since v0.1 cannot honor write access.
		if !d.set.has(path + ".readonly") {
			p.ReadOnly = true
		}
		d.dur(&p.BusyTimeout, path+".busy_timeout", DefaultBusyTimeout)
		d.dur(&p.ChangeCheckInterval, path+".change_check_interval", DefaultChangeCheckInterval)
	case s.HTTP != nil:
		p := s.HTTP
		d.dur(&p.Timeout, path+".timeout", DefaultHTTPTimeout)
		d.num64(&p.MaxResponseBytes, path+".max_response_bytes", DefaultMaxResponseBytes)
		d.num(&p.Retries, path+".retries", DefaultHTTPRetries)
	}
}

func (d defaulter) metric(m *Metric, path string, defs Defaults) {
	cp := path + ".collect"
	if m.Collect.Mode == "" && !d.set.has(cp+".mode") {
		m.Collect.Mode = ModePoll
	}
	d.dur(&m.Collect.Interval, cp+".interval", defs.Collection.Interval.Duration())
	d.dur(&m.Collect.Timeout, cp+".timeout", defs.Collection.Timeout.Duration())
	d.dur(&m.Collect.Debounce, cp+".debounce", defs.Collection.Debounce.Duration())
	d.dur(&m.Collect.Jitter, cp+".jitter", defs.Collection.Jitter.Duration())

	// Reconciliation is mandatory for change-driven modes because notification
	// delivery is not durable (AD-005).
	if m.Collect.Mode == ModeNotify || m.Collect.Mode == ModeAuto {
		d.dur(&m.Collect.FallbackInterval, cp+".fallback_interval", defs.Collection.ReconcileInterval.Duration())
	}

	mp := path + ".mapping"
	if m.Mapping.Scale == 0 && !d.set.has(mp+".scale") {
		m.Mapping.Scale = 1
	}
	if m.Mapping.Null == "" && !d.set.has(mp+".null") {
		m.Mapping.Null = NullFail
	}

	if m.Limits == nil {
		l := defs.Limits
		m.Limits = &l
	} else {
		d.limits(m.Limits, path+".limits", defs.Limits)
	}

	if m.Failure == nil {
		f := defs.Failure
		m.Failure = &f
	} else {
		if m.Failure.Mode == "" && !d.set.has(path+".failure.mode") {
			m.Failure.Mode = defs.Failure.Mode
		}
		d.dur(&m.Failure.MaxStaleAge, path+".failure.max_stale_age", defs.Failure.MaxStaleAge.Duration())
	}

	if m.Request != nil {
		d.str(&m.Request.Method, path+".request.method", http.MethodGet)
	}
}
