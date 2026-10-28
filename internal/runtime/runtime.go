package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
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

// generation is one compiled configuration and the subsystems serving it.
//
// A reload builds a new generation, opens it, then swaps it in. Generations are
// immutable after construction except for the lifecycle fields set at start.
type generation struct {
	cfg            *config.Config
	sources        map[string]source.Source
	store          *snapshot.MemoryStore
	sched          *collect.Scheduler
	watches        []watchGroup
	triggerTargets []triggers.Target

	cancel context.CancelFunc
	done   chan struct{}
}

// Runtime is a configured, unstarted exporter.
type Runtime struct {
	log     *slog.Logger
	metrics *telemetry.Metrics
	store   *storeSlot
	server  *exporter.Server

	gen atomic.Pointer[generation]

	// mu serializes start, reload, and shutdown. Reloads are rare and must not
	// overlap: two generations could otherwise swap sources in either order.
	mu      sync.Mutex
	running bool
	fatal   chan error
}

// New compiles configuration into a runnable exporter.
//
// Does not connect to any source; Run does that. Everything that can fail on
// malformed configuration fails here, before a listener is bound.
func New(cfg *config.Config, log *slog.Logger) (*Runtime, error) {
	if log == nil {
		log = slog.Default()
	}

	registry := prometheus.NewRegistry()
	metrics := telemetry.New(registry, version.Get())

	r := &Runtime{log: log, metrics: metrics, fatal: make(chan error, 4)}
	gen, err := r.buildGeneration(cfg)
	if err != nil {
		return nil, err
	}
	r.store = newStoreSlot(gen.store)
	r.gen.Store(gen)

	server, err := exporter.New(exporter.Options{
		Server:    cfg.Server,
		Store:     r.store,
		Registry:  registry,
		Telemetry: metrics,
		Readiness: exporter.ReadinessFunc(r.readiness),
		Logger:    log,
	})
	if err != nil {
		closeSources(log, gen.sources)
		return nil, fmt.Errorf("exporter: %w", err)
	}
	r.server = server
	return r, nil
}

// buildGeneration resolves secrets, compiles plans, and constructs an unopened
// generation. The caller owns closing the sources if a later step fails.
func (r *Runtime) buildGeneration(cfg *config.Config) (*generation, error) {
	triggerTargets, err := BuildTriggerTargets(cfg)
	if err != nil {
		return nil, err
	}

	sources, err := BuildSources(cfg)
	if err != nil {
		return nil, err
	}

	plans, err := compilePlans(cfg, sources, r.log)
	if err != nil {
		closeSources(r.log, sources)
		return nil, err
	}

	specs := make([]collect.Plan, 0, len(plans))
	for _, p := range plans {
		specs = append(specs, p.plan)
	}
	store := snapshot.NewMemoryStore(collect.StoreConfigFor(specs))

	sched, err := collect.NewScheduler(collect.Options{
		Store:   store,
		Metrics: r.metrics,
		Logger:  r.log,
	})
	if err != nil {
		closeSources(r.log, sources)
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	for _, p := range plans {
		if addErr := sched.Add(p.plan, p.collector); addErr != nil {
			closeSources(r.log, sources)
			return nil, fmt.Errorf("plan %q: %w", p.plan.ID, addErr)
		}
	}

	return &generation{
		cfg: cfg, sources: sources, store: store, sched: sched,
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

func (r *Runtime) readiness() error {
	gen := r.gen.Load()
	if gen == nil {
		return errors.New("runtime is not running")
	}
	return readiness(gen.sched.Status())
}

// Addr reports the bound listener address. Empty until Run binds it.
func (r *Runtime) Addr() string { return r.server.Addr() }

// Run opens every source, then serves until ctx is cancelled.
//
// The listener binds only after every source has been opened: serving a /metrics
// that can never produce samples would present a broken deployment as a healthy one.
func (r *Runtime) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("runtime is already running")
	}
	gen := r.gen.Load()
	if gen == nil {
		r.mu.Unlock()
		return errors.New("runtime has no configuration")
	}
	if err := r.openGeneration(ctx, gen); err != nil {
		r.mu.Unlock()
		closeSources(r.log, gen.sources)
		return err
	}

	// Hold the lifecycle lock through provisioning and the first start so a
	// SIGHUP cannot swap generations before this one is running.
	//
	// Triggers are provisioned before collection starts so the first change is
	// already observable. Failure is non-fatal (§9.5).
	r.provisionManagedTriggers(ctx, gen.triggerTargets)
	r.startGeneration(ctx, gen)
	r.running = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		current := r.gen.Load()
		r.running = false
		r.mu.Unlock()
		r.stopGeneration(context.WithoutCancel(ctx), current, true)
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := r.server.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case r.fatal <- fmt.Errorf("exporter: %w", err):
			default:
			}
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		if err := r.healthLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case r.fatal <- fmt.Errorf("health: %w", err):
			default:
			}
			cancel()
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-r.fatal:
		cancel()
	}
	wg.Wait()
	return runErr
}

// Reload atomically replaces sources, plans, and published state.
//
// The old generation keeps serving while the new one is resolved, compiled, and
// opened. If any of that fails, the old generation is untouched. Server settings are
// fixed for the process lifetime; changing them requires a restart.
func (r *Runtime) Reload(ctx context.Context, cfg *config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running {
		return errors.New("runtime is not running")
	}
	current := r.gen.Load()
	if current == nil {
		return errors.New("runtime has no generation")
	}
	if cfg.Server != current.cfg.Server {
		return errors.New("server settings cannot change on reload; restart metricule")
	}

	next, err := r.buildGeneration(cfg)
	if err != nil {
		return err
	}
	if err := r.openGeneration(ctx, next); err != nil {
		closeSources(r.log, next.sources)
		return err
	}

	// Provisioning failure is logged and does not block the reload: collection still
	// works and reconciliation repairs missed changes (§9.5).
	r.provisionManagedTriggers(ctx, next.triggerTargets)

	// Start before swapping so any immediate publication lands in the next store.
	// The generation's lifetime is the process, not this reload call; Run's shutdown
	// path cancels it explicitly.
	r.startGeneration(context.WithoutCancel(ctx), next)
	r.store.swap(next.store)
	r.gen.Store(next)

	// The old generation publishes through its own store pointer, so it cannot
	// contaminate the new one while it drains.
	r.stopGeneration(ctx, current, false)

	r.log.Info("configuration reloaded",
		"version", version.Get().Version,
		"sources", len(cfg.Sources),
		"metrics", len(cfg.Metrics))
	return nil
}

func (r *Runtime) openGeneration(ctx context.Context, gen *generation) error {
	for _, name := range sortedNames(gen.cfg.Sources) {
		src := gen.sources[name]

		openCtx, cancel := context.WithTimeout(ctx, openTimeout)
		err := src.Open(openCtx)
		cancel()

		if err != nil {
			// A source that cannot be opened is a configuration or connectivity error
			// the operator must see, not a degraded mode.
			return fmt.Errorf("open source %q: %w", name, err)
		}
		r.metrics.SetSourceUp(name, true)
		r.log.Info("source opened", "source", name, "type", src.Type())
	}
	return nil
}

// startGeneration runs collection and change observation until the generation is
// cancelled. Fatal scheduler failures are forwarded to the runtime loop.
func (r *Runtime) startGeneration(parent context.Context, gen *generation) {
	ctx, cancel := context.WithCancel(parent)
	gen.cancel = cancel
	gen.done = make(chan struct{})

	go func() {
		defer close(gen.done)
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := gen.sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case r.fatal <- fmt.Errorf("collection: %w", err):
				default:
				}
				cancel()
			}
		}()

		if len(gen.watches) > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				watchSupervisor(ctx, gen.watches, gen.sched.Invalidate, r.log)
			}()
		}
		wg.Wait()
	}()
}

// stopGeneration drains one generation. Trigger cleanup runs only on process
// shutdown, never on reload: a reload should not delete objects the next
// configuration may recreate.
func (r *Runtime) stopGeneration(ctx context.Context, gen *generation, cleanupTriggers bool) {
	if gen == nil {
		return
	}
	if gen.cancel != nil {
		gen.cancel()
		<-gen.done
	}
	closeSources(r.log, gen.sources)
	if cleanupTriggers {
		r.cleanupManagedTriggers(ctx, gen.triggerTargets)
	}
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
