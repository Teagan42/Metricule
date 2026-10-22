package runtime

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/exporter"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

// Report is the outcome of a single diagnostic collection.
type Report struct {
	Metric string
	Source string
	// Target is sanitized source information, safe to print (§15.2).
	Target     string
	Query      string
	Duration   time.Duration
	Rows       int
	Samples    int
	Exposition string
}

// CollectOnce runs one metric's collection and renders the result.
//
// Backs `metricule test`: §15.2 requires it to execute the collection, show timing
// and sanitized source information, render the exposition, and report mapping
// errors. It builds only the source the metric needs, so testing one metric does not
// require every source to be reachable.
func CollectOnce(ctx context.Context, cfg *config.Config, metric string, log *slog.Logger) (*Report, error) {
	if log == nil {
		log = slog.Default()
	}

	m := findMetric(cfg, metric)
	if m == nil {
		return nil, fmt.Errorf("unknown metric %q (configured: %s)", metric, strings.Join(metricNames(cfg), ", "))
	}

	sc, ok := cfg.Sources[m.Source]
	if !ok {
		return nil, fmt.Errorf("metric %q references unknown source %q", m.Name, m.Source)
	}
	src, err := BuildSource(m.Source, sc)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			log.Warn("closing source failed", "source", m.Source, "error", cerr)
		}
	}()

	openCtx, cancel := context.WithTimeout(ctx, openTimeout)
	err = src.Open(openCtx)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("open source %q: %w", m.Source, err)
	}

	plans, err := compilePlans(&config.Config{
		Sources: map[string]config.SourceConfig{m.Source: sc},
		Metrics: []config.Metric{*m},
		Server:  cfg.Server,
	}, map[string]source.Source{m.Source: src}, log)
	if err != nil {
		return nil, err
	}
	p := plans[0]

	runCtx, cancelRun := context.WithTimeout(ctx, m.Collect.Timeout.Duration())
	defer cancelRun()

	result, err := p.collector.Collect(runCtx)
	if err != nil {
		return nil, err
	}

	exposition, err := render(result)
	if err != nil {
		return nil, err
	}

	return &Report{
		Metric:     m.Name,
		Source:     m.Source,
		Target:     target(src),
		Query:      p.plan.Spec.Describe(),
		Duration:   result.Duration,
		Rows:       result.RowCount,
		Samples:    result.Snapshot.SampleCount(),
		Exposition: exposition,
	}, nil
}

// Renders one collection as Prometheus exposition, through the same store and
// collector the exporter uses, so `test` output matches what a scrape would return.
func render(result collect.Result) (string, error) {
	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	if err := store.Publish(result.Snapshot); err != nil {
		return "", fmt.Errorf("publish: %w", err)
	}

	reg := prometheus.NewRegistry()
	if err := reg.Register(exporter.NewCollector(store, time.Now)); err != nil {
		return "", fmt.Errorf("register collector: %w", err)
	}

	families, err := reg.Gather()
	if err != nil {
		return "", fmt.Errorf("gather: %w", err)
	}

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, f := range families {
		if err := enc.Encode(f); err != nil {
			return "", fmt.Errorf("encode %s: %w", f.GetName(), err)
		}
	}
	return buf.String(), nil
}

// Reports sanitized source information where the adapter offers it.
func target(src source.Source) string {
	if d, ok := src.(source.Diagnosable); ok {
		if t := d.Diagnostics().Target; t != "" {
			return t
		}
	}
	return src.Type()
}

func findMetric(cfg *config.Config, name string) *config.Metric {
	for i := range cfg.Metrics {
		if cfg.Metrics[i].Name == name {
			return &cfg.Metrics[i]
		}
	}
	return nil
}

func metricNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Metrics))
	for _, m := range cfg.Metrics {
		out = append(out, m.Name)
	}
	return out
}
