package runtime

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/source"
)

// compiled is one plan and the collector that executes it.
type compiled struct {
	plan      collect.Plan
	collector collect.Collector
}

// compilePlans turns validated configuration into executable plans.
//
// One plan per metric. §7.3 permits metrics sharing an identical source query to
// share execution, but that is an optimization and not required: for a document
// source the selectors are part of the compiled query, so two metrics reading the
// same path are not necessarily the same query. Sharing can be added later without
// changing this interface.
func compilePlans(cfg *config.Config, sources map[string]source.Source, log *slog.Logger) ([]compiled, error) {
	out := make([]compiled, 0, len(cfg.Metrics))

	for i := range cfg.Metrics {
		m := &cfg.Metrics[i]

		src, ok := sources[m.Source]
		if !ok {
			return nil, fmt.Errorf("metric %q: no source named %q", m.Name, m.Source)
		}

		spec, err := src.CompileQuery(queryDefinition(m))
		if err != nil {
			return nil, fmt.Errorf("metric %q: %w", m.Name, err)
		}

		group, err := mapping.CompileGroup(mapping.GroupSpec{
			CollectionID: m.Name,
			SourceID:     m.Source,
			Metrics:      []config.Metric{*m},
			Limits:       *m.Limits,
		})
		if err != nil {
			return nil, fmt.Errorf("metric %q: %w", m.Name, err)
		}

		plan := collect.Plan{
			ID:        m.Name,
			SourceID:  m.Source,
			Spec:      spec,
			Metrics:   []config.Metric{*m},
			Mode:      m.Collect.Mode,
			Interval:  m.Collect.Interval.Duration(),
			Timeout:   m.Collect.Timeout.Duration(),
			Debounce:  m.Collect.Debounce.Duration(),
			Jitter:    m.Collect.Jitter.Duration(),
			Reconcile: m.Collect.FallbackInterval.Duration(),
			Limits:    *m.Limits,
			Failure:   *m.Failure,
		}
		if w := m.Collect.Watch; w != nil {
			plan.Watch = &source.WatchSpec{
				Channel: m.Collect.Channel,
				Tables:  w.Tables,
				Events:  w.Events,
			}
		} else if m.Collect.Channel != "" {
			plan.Watch = &source.WatchSpec{Channel: m.Collect.Channel}
		}

		warnDegradedMode(log, m, src)

		out = append(out, compiled{
			plan: plan,
			collector: &collector{
				id:       m.Name,
				sourceID: m.Source,
				src:      src,
				spec:     spec,
				group:    group,
				maxRows:  m.Limits.MaxRowsPerQuery,
				now:      time.Now,
			},
		})
	}

	return out, nil
}

// Warns when a plan asks for a mechanism its source instance cannot provide.
//
// Offline validation checks the declared capability table, which records what the
// data source supports; the constructed instance reports what this build implements.
// Wiring is the only place holding both, so it is the only place that can notice.
//
// Not an error: the configuration is forward-valid, and §9.5 permits explicitly
// sanctioned fallback polling. Reconciliation is mandatory for change-driven modes,
// so the plan still refreshes — just on its fallback interval rather than on change.
func warnDegradedMode(log *slog.Logger, m *config.Metric, src source.Source) {
	caps := src.Capabilities()
	switch m.Collect.Mode {
	case config.ModeNotify:
		if !caps.Notifications {
			log.Warn("collection mode degraded to reconcile-only",
				"metric", m.Name,
				"source", m.Source,
				"requested", string(m.Collect.Mode),
				"reason", "this build implements no change observation for the source type",
				"refresh_interval", m.Collect.FallbackInterval.String())
		}
	case config.ModeAuto:
		if !caps.Notifications && !caps.ChangeTokens {
			log.Info("collection mode resolved to polling",
				"metric", m.Name,
				"source", m.Source,
				"refresh_interval", m.Collect.FallbackInterval.String())
		}
	}
}

// Builds the source-agnostic query definition for a metric.
func queryDefinition(m *config.Metric) source.QueryDefinition {
	def := source.QueryDefinition{
		MetricName: m.Name,
		Rows:       m.Mapping.Rows,
		Value:      m.Mapping.Value,
		Labels:     m.Mapping.Labels,
	}
	if m.Query != nil {
		def.SQL = m.Query.SQL
	}
	if m.Request != nil {
		def.Method = m.Request.Method
		def.Path = m.Request.Path
		def.Params = m.Request.Params
		def.Headers = m.Request.Headers
	}
	return def
}
