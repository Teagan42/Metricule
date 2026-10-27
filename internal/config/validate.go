package config

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/teagan42/metricule/internal/selector"
	"github.com/teagan42/metricule/internal/source"
)

// Prometheus identifier rules. Deliberately the legacy-strict forms: §8.6 requires
// conventional snake_case names, and accepting UTF-8 names would let configuration
// emit series that older scrapers cannot parse.
var (
	metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRe  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// TelemetryPrefix is reserved for Metricule's own metric families (§13).
const TelemetryPrefix = "metricule_"

// stateLabel is generated for state_set metrics and cannot be user-assigned.
const stateLabel = "state"

// CapabilityFunc reports an adapter type's capabilities.
//
// Injected so that configuration validation stays testable without linking adapters.
type CapabilityFunc func(sourceType string) (source.Capabilities, bool)

// Validate runs offline validation. It never connects to a source (§15.1).
//
// Pass nil caps to skip adapter capability checks; callers that serve traffic must
// pass source.CapabilitiesFor so unsupported collection modes are rejected.
func Validate(c *Config, caps CapabilityFunc) Diagnostics {
	v := &collector{}

	validateHeader(v, c)
	validateServer(v, c)
	validateLimits(v, "defaults.limits", c.Defaults.Limits)
	validateFailure(v, "defaults.failure", c.Defaults.Failure)
	validateSources(v, c)
	validateMetrics(v, c, caps)

	return v.ds.Sorted()
}

func validateHeader(v *collector, c *Config) {
	if c.APIVersion == "" {
		v.errorf("apiVersion", "required field is missing")
	} else if c.APIVersion != APIVersion {
		v.errorf("apiVersion", "unsupported version %q, expected %q", c.APIVersion, APIVersion)
	}
	if c.Kind == "" {
		v.errorf("kind", "required field is missing")
	} else if c.Kind != Kind {
		v.errorf("kind", "unsupported kind %q, expected %q", c.Kind, Kind)
	}
	if len(c.Sources) == 0 {
		v.errorf("sources", "at least one source is required")
	}
	if len(c.Metrics) == 0 {
		v.errorf("metrics", "at least one metric is required")
	}
}

func validateServer(v *collector, c *Config) {
	if c.Server.Listen == "" {
		v.errorf("server.listen", "required field is missing")
	}
	paths := map[string]string{
		"server.metrics_path":   c.Server.MetricsPath,
		"server.health_path":    c.Server.HealthPath,
		"server.readiness_path": c.Server.ReadinessPath,
	}
	seen := map[string]string{}
	for _, field := range sortedKeys(paths) {
		p := paths[field]
		if !strings.HasPrefix(p, "/") {
			v.errorf(field, "path %q must begin with /", p)
			continue
		}
		if prev, dup := seen[p]; dup {
			v.errorf(field, "path %q collides with %s", p, prev)
			continue
		}
		seen[p] = field
	}
}

func validateLimits(v *collector, path string, l Limits) {
	checks := map[string]int{
		"max_series_per_metric":      l.MaxSeriesPerMetric,
		"max_rows_per_query":         l.MaxRowsPerQuery,
		"max_concurrent_collections": l.MaxConcurrentCollections,
		"max_total_series":           l.MaxTotalSeries,
		"max_metrics":                l.MaxMetrics,
		"max_label_value_bytes":      l.MaxLabelValueBytes,
	}
	for _, k := range sortedKeys(checks) {
		if checks[k] <= 0 {
			v.errorf(path+"."+k, "must be greater than zero, got %d", checks[k])
		}
	}
	if l.MaxSeriesPerMetric > l.MaxTotalSeries {
		v.errorf(path+".max_series_per_metric",
			"%d exceeds max_total_series %d, so a single metric could consume the global budget",
			l.MaxSeriesPerMetric, l.MaxTotalSeries)
	}
}

func validateFailure(v *collector, path string, f Failure) {
	switch f.Mode {
	case FailureRetain:
		if f.MaxStaleAge <= 0 {
			v.errorf(path+".max_stale_age", "retain mode requires a positive max_stale_age")
		}
	case FailureDrop:
	case "":
		v.errorf(path+".mode", "required field is missing")
	default:
		v.errorf(path+".mode", "unsupported mode %q (supported: retain, drop)", f.Mode)
	}
}

func validateSources(v *collector, c *Config) {
	for _, name := range sortedKeys(c.Sources) {
		path := fmt.Sprintf("sources.%s", name)
		s := c.Sources[name]

		if !labelNameRe.MatchString(name) {
			v.errorf(path, "source name %q must match %s because it is used as a telemetry label", name, labelNameRe)
		}

		switch {
		case s.Postgres != nil:
			validatePostgres(v, path, s.Postgres)
		case s.SQLite != nil:
			validateSQLite(v, path, s.SQLite)
		case s.HTTP != nil:
			validateHTTP(v, path, s.HTTP)
		default:
			v.errorf(path, "source has no settings for type %q", s.Type)
		}
	}
}

func validatePostgres(v *collector, path string, p *PostgresSource) {
	if err := p.DSN.Validate(); err != nil {
		v.errorf(path+".dsn", "%s", err)
	}
	if p.Pool.MaxOpen <= 0 {
		v.errorf(path+".pool.max_open", "must be greater than zero")
	}
	if p.Pool.MaxIdle < 0 {
		v.errorf(path+".pool.max_idle", "must not be negative")
	}
	if p.Pool.MaxIdle > p.Pool.MaxOpen {
		v.errorf(path+".pool.max_idle", "%d exceeds max_open %d", p.Pool.MaxIdle, p.Pool.MaxOpen)
	}
	if p.QueryTimeout <= 0 {
		v.errorf(path+".query_timeout", "must be greater than zero")
	}
	if p.TLS != nil && p.TLS.Mode != "" {
		switch p.TLS.Mode {
		case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		default:
			v.errorf(path+".tls.mode", "unsupported sslmode %q", p.TLS.Mode)
		}
	}
	if p.Triggers != nil && p.Triggers.CleanupOnShutdown && !p.Triggers.Manage {
		v.errorf(path+".triggers.cleanup_on_shutdown", "requires triggers.manage: true; nothing is provisioned to clean up")
	}
	if p.Triggers != nil && p.Triggers.Manage {
		if p.Triggers.Schema == "" {
			v.errorf(path+".triggers.schema", "required when triggers.manage is true")
		} else if !labelNameRe.MatchString(p.Triggers.Schema) {
			v.errorf(path+".triggers.schema", "schema %q is not a plain identifier", p.Triggers.Schema)
		}
		if !p.Triggers.DSN.IsZero() {
			if err := p.Triggers.DSN.Validate(); err != nil {
				v.errorf(path+".triggers.dsn", "%s", err)
			}
		} else {
			v.warnf(path+".triggers.dsn",
				"trigger management will use the collection DSN, which should not hold schema-owner privileges (§14.2)")
		}
	}
}

func validateSQLite(v *collector, path string, s *SQLiteSource) {
	if s.Path == "" {
		v.errorf(path+".path", "required field is missing")
	}
	// AD-007 fixes v0.1 to read-only access. Accepting `readonly: false` and
	// quietly reading anyway would advertise a capability that does not exist.
	if !s.ReadOnly {
		v.errorf(path+".readonly", "must be true: v0.1 supports read-only SQLite access only (AD-007)")
	}
	if s.BusyTimeout <= 0 {
		v.errorf(path+".busy_timeout", "must be greater than zero")
	}
	if s.ChangeCheckInterval <= 0 {
		v.errorf(path+".change_check_interval", "must be greater than zero")
	}
}

func validateHTTP(v *collector, path string, h *HTTPSource) {
	if h.BaseURL == "" {
		v.errorf(path+".base_url", "required field is missing")
		return
	}
	u, err := url.Parse(h.BaseURL)
	switch {
	case err != nil:
		v.errorf(path+".base_url", "invalid URL: %s", err)
	case u.Scheme != "http" && u.Scheme != "https":
		v.errorf(path+".base_url", "scheme must be http or https, got %q", u.Scheme)
	case u.Host == "":
		v.errorf(path+".base_url", "URL has no host")
	}
	if h.Timeout <= 0 {
		v.errorf(path+".timeout", "must be greater than zero")
	}
	if h.MaxResponseBytes <= 0 {
		v.errorf(path+".max_response_bytes", "must be greater than zero")
	}
	if h.MaxRedirects < 0 {
		v.errorf(path+".max_redirects", "must not be negative")
	}
	if h.Retries < 0 {
		v.errorf(path+".retries", "must not be negative")
	}
	for i, p := range h.AllowedPaths {
		if !strings.HasPrefix(p, "/") {
			v.errorf(fmt.Sprintf("%s.allowed_paths[%d]", path, i), "path %q must begin with /", p)
		}
	}
	if a := h.Authentication; a != nil {
		switch a.Type {
		case AuthBearer:
			if err := a.Token.Validate(); err != nil {
				v.errorf(path+".authentication.token", "%s", err)
			}
			if a.Username != "" || !a.Password.IsZero() {
				v.errorf(path+".authentication", "bearer authentication does not use username or password")
			}
		case AuthBasic:
			if a.Username == "" {
				v.errorf(path+".authentication.username", "required for basic authentication")
			}
			if err := a.Password.Validate(); err != nil {
				v.errorf(path+".authentication.password", "%s", err)
			}
			if !a.Token.IsZero() {
				v.errorf(path+".authentication", "basic authentication does not use token")
			}
		case "":
			v.errorf(path+".authentication.type", "required field is missing")
		default:
			v.errorf(path+".authentication.type", "unsupported type %q (supported: bearer, basic)", a.Type)
		}
	}
}

func validateMetrics(v *collector, c *Config, caps CapabilityFunc) {
	if len(c.Metrics) > c.Defaults.Limits.MaxMetrics {
		v.errorf("metrics", "%d metrics exceeds defaults.limits.max_metrics %d",
			len(c.Metrics), c.Defaults.Limits.MaxMetrics)
	}

	seen := map[string]int{}
	for i := range c.Metrics {
		m := &c.Metrics[i]
		path := fmt.Sprintf("metrics[%d]", i)

		validateMetricName(v, path, m, seen, i)
		validateMetricSource(v, path, m, c, caps)
		validateMapping(v, path, m, c)
		validateCollect(v, path, m)

		if m.Help == "" {
			v.errorf(path+".help", "required field is missing")
		}
		if m.Limits != nil {
			validateLimits(v, path+".limits", *m.Limits)
		}
		if m.Failure != nil {
			validateFailure(v, path+".failure", *m.Failure)
		}
	}
}

func validateMetricName(v *collector, path string, m *Metric, seen map[string]int, idx int) {
	if m.Name == "" {
		v.errorf(path+".name", "required field is missing")
		return
	}
	if !metricNameRe.MatchString(m.Name) {
		v.errorf(path+".name", "metric name %q must match %s", m.Name, metricNameRe)
	}
	if strings.HasPrefix(m.Name, TelemetryPrefix) {
		v.errorf(path+".name", "prefix %q is reserved for Metricule's own telemetry", TelemetryPrefix)
	}
	if prev, dup := seen[m.Name]; dup {
		v.errorf(path+".name", "duplicate metric name %q, already declared by metrics[%d]", m.Name, prev)
	} else {
		seen[m.Name] = idx
	}

	// _bucket and _sum belong to histogram and summary families, which v0.1 does not
	// emit. Reusing them would collide with a future real histogram (§8.6).
	for _, suffix := range []string{"_bucket", "_sum"} {
		if strings.HasSuffix(m.Name, suffix) {
			v.errorf(path+".name", "suffix %q is reserved for histogram and summary families", suffix)
		}
	}
	if m.Type == MetricCounter && !strings.HasSuffix(m.Name, "_total") {
		v.warnf(path+".name", "counters conventionally end with _total")
	}
	if m.Name != strings.ToLower(m.Name) {
		v.warnf(path+".name", "metric names are conventionally lowercase snake_case")
	}
}

func validateMetricSource(v *collector, path string, m *Metric, c *Config, caps CapabilityFunc) {
	if m.Source == "" {
		v.errorf(path+".source", "required field is missing")
		return
	}
	s, ok := c.Sources[m.Source]
	if !ok {
		v.errorf(path+".source", "unknown source %q (declared: %v)", m.Source, sortedKeys(c.Sources))
		return
	}

	// A source's query shape is fixed by its type. Accepting the wrong block would
	// mean advertising configuration the adapter cannot execute (rule 3).
	switch s.Type {
	case SourcePostgres, SourceSQLite:
		if m.Query == nil {
			v.errorf(path+".query", "source %q is type %s and requires a query block", m.Source, s.Type)
		} else if strings.TrimSpace(m.Query.SQL) == "" {
			v.errorf(path+".query.sql", "required field is empty")
		}
		if m.Request != nil {
			v.errorf(path+".request", "source %q is type %s and does not accept a request block", m.Source, s.Type)
		}
	case SourceHTTP:
		if m.Request == nil {
			v.errorf(path+".request", "source %q is type http and requires a request block", m.Source)
		} else {
			validateRequest(v, path+".request", m.Request, s.HTTP)
		}
		if m.Query != nil {
			v.errorf(path+".query", "source %q is type http and does not accept a query block", m.Source)
		}
	}

	if caps == nil {
		return
	}
	adapterCaps, ok := caps(string(s.Type))
	if !ok {
		v.errorf(path+".source", "source type %q has no registered adapter in this build", s.Type)
		return
	}
	validateModeAgainstCapabilities(v, path, m, s.Type, adapterCaps)
}

func validateModeAgainstCapabilities(v *collector, path string, m *Metric, st SourceType, c source.Capabilities) {
	switch m.Collect.Mode {
	case ModePoll:
		if !c.Poll {
			v.errorf(path+".collect.mode", "source type %q does not support polling", st)
		}
	case ModeNotify:
		if !c.Notifications {
			hint := ""
			if c.ChangeTokens {
				hint = `; use mode "auto" to use its change-token detection`
			}
			v.errorf(path+".collect.mode", "source type %q does not support notifications%s", st, hint)
		}
		validateNotificationScope(v, path, m, st)
	case ModeAuto:
		if !c.Notifications && !c.ChangeTokens && !c.Poll {
			v.errorf(path+".collect.mode", "source type %q advertises no collection mechanism", st)
		}
		switch {
		case c.Notifications && m.Collect.Channel == "":
			v.errorf(path+".collect.channel", "auto mode on source type %q resolves to notifications and requires a channel", st)
		case !c.Notifications && m.Collect.Channel != "":
			v.errorf(path+".collect.channel", "source type %q does not support notification channels", st)
		}
		if c.Notifications {
			validateNotificationScope(v, path, m, st)
		}
	}
}

// Checks the scope a PostgreSQL notification plan depends on.
//
// The adapter derives notification scope from a schema-qualified table name, and
// managed triggers address identifiers directly. An unqualified or quoted name
// would need a silent guess, so it is rejected at validation time.
func validateNotificationScope(v *collector, path string, m *Metric, st SourceType) {
	if st != SourcePostgres {
		return
	}
	if channel := m.Collect.Channel; channel != "" && len([]byte(channel)) > 63 {
		v.errorf(path+".collect.channel", "channel %q exceeds PostgreSQL's 63-byte identifier limit", channel)
	}
	if m.Collect.Watch == nil {
		return
	}
	for i, table := range m.Collect.Watch.Tables {
		parts := strings.Split(table, ".")
		if len(parts) != 2 || !labelNameRe.MatchString(parts[0]) || !labelNameRe.MatchString(parts[1]) {
			v.errorf(fmt.Sprintf("%s.collect.watch.tables[%d]", path, i),
				"table %q must be schema-qualified with plain identifiers, such as public.jobs", table)
		}
	}
}

func validateRequest(v *collector, path string, r *Request, h *HTTPSource) {
	if r.Method != "" && r.Method != http.MethodGet {
		v.errorf(path+".method", "only GET is supported in v0.1, got %q", r.Method)
	}
	if r.Path == "" {
		v.errorf(path+".path", "required field is missing")
		return
	}
	if !strings.HasPrefix(r.Path, "/") {
		v.errorf(path+".path", "path %q must begin with /", r.Path)
	}
	if h == nil || len(h.AllowedPaths) == 0 {
		return
	}
	for _, allowed := range h.AllowedPaths {
		if r.Path == allowed {
			return
		}
	}
	v.errorf(path+".path", "path %q is not in the source's allowed_paths %v", r.Path, h.AllowedPaths)
}

func validateMapping(v *collector, path string, m *Metric, c *Config) {
	mp := path + ".mapping"
	isJSON := c.Sources[m.Source].Type == SourceHTTP

	switch m.Type {
	case MetricGauge, MetricCounter:
		if len(m.Mapping.States) > 0 {
			v.errorf(mp+".states", "states apply only to state_set metrics")
		}
	case MetricStateSet:
		if len(m.Mapping.States) == 0 {
			v.errorf(mp+".states", "state_set metrics require an explicit state list")
		}
		if _, conflict := m.Mapping.Labels[stateLabel]; conflict {
			v.errorf(mp+".labels."+stateLabel, "label %q is generated for state_set metrics", stateLabel)
		}
		if _, conflict := m.Mapping.StaticLabels[stateLabel]; conflict {
			v.errorf(mp+".static_labels."+stateLabel, "label %q is generated for state_set metrics", stateLabel)
		}
		seenState := map[string]bool{}
		for i, st := range m.Mapping.States {
			if st == "" {
				v.errorf(fmt.Sprintf("%s.states[%d]", mp, i), "state must not be empty")
			}
			if seenState[st] {
				v.errorf(fmt.Sprintf("%s.states[%d]", mp, i), "duplicate state %q", st)
			}
			seenState[st] = true
		}
	case "":
		v.errorf(path+".type", "required field is missing")
	default:
		v.errorf(path+".type", "unsupported type %q (supported: gauge, counter, state_set)", m.Type)
	}

	if m.Mapping.Value == "" {
		v.errorf(mp+".value", "required field is missing")
	} else if isJSON {
		if err := selector.Valid(m.Mapping.Value); err != nil {
			v.errorf(mp+".value", "%s", err)
		}
	}
	if m.Mapping.Rows != "" {
		if !isJSON {
			v.errorf(mp+".rows", "rows selection applies only to http sources; SQL results are already row-oriented")
		} else if err := selector.Valid(m.Mapping.Rows); err != nil {
			v.errorf(mp+".rows", "%s", err)
		}
	}

	for _, name := range sortedKeys(m.Mapping.Labels) {
		validateLabelName(v, mp+".labels", name)
		if isJSON {
			if err := selector.Valid(m.Mapping.Labels[name]); err != nil {
				v.errorf(mp+".labels."+name, "%s", err)
			}
		} else if m.Mapping.Labels[name] == "" {
			v.errorf(mp+".labels."+name, "column name must not be empty")
		}
	}
	for _, name := range sortedKeys(m.Mapping.StaticLabels) {
		validateLabelName(v, mp+".static_labels", name)
		if _, dup := m.Mapping.Labels[name]; dup {
			v.errorf(mp+".static_labels."+name, "label %q is also declared in labels", name)
		}
	}

	// A zero multiplier turns every sample into a fabricated zero, which is exactly
	// what AD-010 forbids.
	if m.Mapping.Scale == 0 {
		v.errorf(mp+".scale", "must not be zero: every sample would become zero")
	}

	switch m.Mapping.Null {
	case NullFail, NullOmit:
	default:
		v.errorf(mp+".null", "unsupported policy %q (supported: fail, omit)", m.Mapping.Null)
	}
}

func validateLabelName(v *collector, path, name string) {
	switch {
	case name == "":
		v.errorf(path, "label name must not be empty")
	case !labelNameRe.MatchString(name):
		v.errorf(path+"."+name, "label name %q must match %s", name, labelNameRe)
	case strings.HasPrefix(name, "__"):
		v.errorf(path+"."+name, "the __ prefix is reserved by Prometheus")
	}
}

func validateCollect(v *collector, path string, m *Metric) {
	cp := path + ".collect"
	switch m.Collect.Mode {
	case ModePoll:
		if m.Collect.Interval <= 0 {
			v.errorf(cp+".interval", "poll mode requires a positive interval")
		}
		if m.Collect.Channel != "" {
			v.errorf(cp+".channel", "channel applies only to notify mode")
		}
		if m.Collect.Watch != nil {
			v.errorf(cp+".watch", "watch applies only to notify and auto modes")
		}
		if m.Collect.RequireNotifications {
			v.errorf(cp+".require_notifications", "require_notifications applies only to notify and auto modes")
		}
	case ModeNotify:
		if m.Collect.Channel == "" {
			v.errorf(cp+".channel", "notify mode requires a channel")
		}
		// Reconciliation repairs lost notifications, which are not durable (AD-005).
		if m.Collect.FallbackInterval <= 0 {
			v.errorf(cp+".fallback_interval", "notify mode requires a positive fallback_interval for reconciliation")
		}
	case ModeAuto:
		if m.Collect.FallbackInterval <= 0 {
			v.errorf(cp+".fallback_interval", "auto mode requires a positive fallback_interval for reconciliation")
		}
	case ModeManual, ModeCDC:
		v.errorf(cp+".mode", "mode %q is reserved for a future release and is not implemented in v0.1", m.Collect.Mode)
	case "":
		v.errorf(cp+".mode", "required field is missing")
	default:
		v.errorf(cp+".mode", "unsupported mode %q (supported: poll, notify, auto)", m.Collect.Mode)
	}

	if m.Collect.Timeout <= 0 {
		v.errorf(cp+".timeout", "must be greater than zero")
	}
	if m.Collect.Debounce < 0 {
		v.errorf(cp+".debounce", "must not be negative")
	}
	if m.Collect.Jitter < 0 {
		v.errorf(cp+".jitter", "must not be negative")
	}
	if m.Collect.Mode == ModePoll && m.Collect.Timeout >= m.Collect.Interval {
		v.warnf(cp+".timeout", "timeout %s is not shorter than interval %s, so collections can overlap continuously",
			m.Collect.Timeout, m.Collect.Interval)
	}
	if w := m.Collect.Watch; w != nil {
		for i, e := range w.Events {
			switch strings.ToLower(e) {
			case "insert", "update", "delete":
			default:
				v.errorf(fmt.Sprintf("%s.watch.events[%d]", cp, i), "unsupported event %q (supported: insert, update, delete)", e)
			}
		}
		for i, t := range w.Tables {
			if strings.TrimSpace(t) == "" {
				v.errorf(fmt.Sprintf("%s.watch.tables[%d]", cp, i), "table must not be empty")
			}
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
