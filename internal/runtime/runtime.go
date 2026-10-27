package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/exporter"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/triggers"
	"github.com/teagan42/metricule/internal/version"
)

// Bounds a source's initial connection attempt so startup cannot hang.
const openTimeout = 30 * time.Second

// Runtime is a configured, unstarted exporter.
type Runtime struct {
	cfg            *config.Config
	log            *slog.Logger
	sources        map[string]source.Source
	store          *snapshot.MemoryStore
	sched          *collect.Scheduler
	server         *exporter.Server
	metrics        *telemetry.Metrics
	watches        []watchGroup
	triggerTargets []triggers.Target
}

// New compiles configuration into a runnable exporter.
//
// Does not connect to any source; Run does that. Everything that can fail on
// malformed configuration fails here, before a listener is bound.
func New(cfg *config.Config, log *slog.Logger) (*Runtime, error) {
	if log == nil {
		log = slog.Default()
	}

	triggerTargets, err := BuildTriggerTargets(cfg)
	if err != nil {
		return nil, err
	}

	registry := prometheus.NewRegistry()
	metrics := telemetry.New(registry, version.Get())

	sources, err := BuildSources(cfg)
	if err != nil {
		return nil, err
	}

	plans, err := compilePlans(cfg, sources, log)
	if err != nil {
		closeSources(log, sources)
		return nil, err
	}

	specs := make([]collect.Plan, 0, len(plans))
	for _, p := range plans {
		specs = append(specs, p.plan)
	}
	store := snapshot.NewMemoryStore(collect.StoreConfigFor(specs))

	sched, err := collect.NewScheduler(collect.Options{
		Store:   store,
		Metrics: metrics,
		Logger:  log,
	})
	if err != nil {
		closeSources(log, sources)
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	for _, p := range plans {
		if addErr := sched.Add(p.plan, p.collector); addErr != nil {
			closeSources(log, sources)
			return nil, fmt.Errorf("plan %q: %w", p.plan.ID, addErr)
		}
	}

	server, err := exporter.New(exporter.Options{
		Server:    cfg.Server,
		Store:     store,
		Registry:  registry,
		Telemetry: metrics,
		Readiness: exporter.ReadinessFunc(func() error { return readiness(sched.Status()) }),
		Logger:    log,
	})
	if err != nil {
		closeSources(log, sources)
		return nil, fmt.Errorf("exporter: %w", err)
	}

	return &Runtime{
		cfg: cfg, log: log, sources: sources,
		store: store, sched: sched, server: server, metrics: metrics,
		watches:        buildWatchGroups(plans, sources),
		triggerTargets: triggerTargets,
	}, nil
}

// Readiness covers configuration and subsystem initialization, plus the first
// collection attempt per plan — not upstream health (§13.1).
//
// Attempted rather than succeeded: a source that is down must not hold the process
// unready forever, because /metrics is still serving valid exposition and the
// failure is already visible through collection telemetry (P3).
func readiness(st collect.Status) error {
	if st.Attempted < st.Plans {
		return fmt.Errorf("%d of %d collections have not yet been attempted", st.Plans-st.Attempted, st.Plans)
	}
	return nil
}

// Addr reports the bound listener address. Empty until Run binds it.
func (r *Runtime) Addr() string { return r.server.Addr() }

// Run opens every source, then serves until ctx is cancelled.
//
// The listener binds only after every source has been opened: serving a /metrics
// that can never produce samples would present a broken deployment as a healthy one.
func (r *Runtime) Run(ctx context.Context) error {
	if err := r.openSources(ctx); err != nil {
		closeSources(r.log, r.sources)
		return err
	}
	defer closeSources(r.log, r.sources)
	defer r.cleanupManagedTriggers(context.WithoutCancel(ctx))

	// Triggers are provisioned before collection starts so the first change is
	// already observable. Failure is non-fatal (§9.5).
	r.provisionManagedTriggers(ctx)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 4)

	run := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errs <- fmt.Errorf("%s: %w", name, err)
				cancel()
			}
		}()
	}

	run("collection", r.sched.Run)
	run("exporter", r.server.ListenAndServe)
	run("health", r.healthLoop)
	if len(r.watches) > 0 {
		run("watch", func(ctx context.Context) error {
			watchSupervisor(ctx, r.watches, r.sched.Invalidate, r.log)
			return nil
		})
	}

	wg.Wait()
	close(errs)

	// Report the first real failure; a cancelled sibling is not one.
	for err := range errs {
		return err
	}
	return nil
}

func (r *Runtime) openSources(ctx context.Context) error {
	for _, name := range sortedNames(r.cfg.Sources) {
		src := r.sources[name]

		openCtx, cancel := context.WithTimeout(ctx, openTimeout)
		err := src.Open(openCtx)
		cancel()

		if err != nil {
			// A source that cannot be opened at startup is a configuration or
			// connectivity error the operator must see, not a degraded mode.
			return fmt.Errorf("open source %q: %w", name, err)
		}
		r.metrics.SetSourceUp(name, true)
		r.log.Info("source opened", "source", name, "type", src.Type())
	}
	return nil
}

func closeSources(log *slog.Logger, sources map[string]source.Source) {
	for _, name := range sortedSourceNames(sources) {
		if err := sources[name].Close(); err != nil {
			log.Warn("closing source failed", "source", name, "error", err)
		}
	}
}

func sortedSourceNames(m map[string]source.Source) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
