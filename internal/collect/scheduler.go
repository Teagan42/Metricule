package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
)

// Options configures a Scheduler.
type Options struct {
	// Store receives published snapshots. Required.
	Store snapshot.Store
	// Metrics records collection telemetry. Required.
	Metrics *telemetry.Metrics
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now. Injected so staleness telemetry is testable.
	Now func() time.Time
	// Retry bounds failure handling. Zero fields take package defaults.
	Retry RetryPolicy
	// Jitter returns a fraction in [0,1) scaling each plan's jitter window. Defaults
	// to a pseudo-random source; overridden to make schedules deterministic in tests.
	Jitter func() float64
}

// Scheduler drives collection for a fixed set of plans.
//
// Plans are registered before Run and never change afterwards, so a plan's
// configuration cannot shift under a running collection.
type Scheduler struct {
	opts    Options
	log     *slog.Logger
	now     func() time.Time
	runners []*runner

	mu      sync.Mutex
	started bool
}

// ErrAlreadyStarted rejects registration after Run has been called.
var ErrAlreadyStarted = errors.New("scheduler already started")

// NewScheduler validates options and returns a scheduler with no plans.
func NewScheduler(opts Options) (*Scheduler, error) {
	if opts.Store == nil {
		return nil, errors.New("collect: Options.Store is required")
	}
	if opts.Metrics == nil {
		return nil, errors.New("collect: Options.Metrics is required")
	}
	opts.Retry = opts.Retry.withDefaults()

	s := &Scheduler{opts: opts, log: opts.Logger, now: opts.Now}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Returns the jitter fraction for the next interval.
//
// math/rand/v2 is deliberate: spreading query schedules is a load concern, not a
// security one, and its top-level source is safe for concurrent use.
func (s *Scheduler) jitter() float64 {
	if s.opts.Jitter != nil {
		return s.opts.Jitter()
	}
	return rand.Float64()
}

// Add registers a plan and the collector that executes it.
//
// Must be called before Run.
func (s *Scheduler) Add(plan Plan, c Collector) error {
	if plan.ID == "" {
		return errors.New("collect: plan has no ID")
	}
	if c == nil {
		return fmt.Errorf("collect: plan %q has no collector", plan.ID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("collect: plan %q: %w", plan.ID, ErrAlreadyStarted)
	}
	for _, r := range s.runners {
		if r.plan.ID == plan.ID {
			return fmt.Errorf("collect: duplicate plan %q", plan.ID)
		}
	}
	s.runners = append(s.runners, newRunner(s, plan, c))
	return nil
}

// Run collects until ctx is cancelled, then waits for in-flight work to unwind.
//
// Returns nil on cancellation: an operator-requested shutdown is not an error.
func (s *Scheduler) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return ErrAlreadyStarted
	}
	s.started = true
	runners := s.runners
	s.mu.Unlock()

	for src, sem := range sourceSemaphores(runners) {
		for _, r := range runners {
			if r.plan.SourceID == src {
				r.sem = sem
			}
		}
	}

	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Add(1)
		go func(r *runner) {
			defer wg.Done()
			r.run(ctx)
		}(r)
	}
	wg.Wait()
	return nil
}

// Builds one capacity-limited channel per source.
//
// The cap is the strictest limit among the source's plans: a metric configured to
// allow one concurrent collection is a statement about the source, not just itself.
// Channels are fixed-size, so backpressure blocks a runner instead of queueing work
// (rule 10).
func sourceSemaphores(runners []*runner) map[string]chan struct{} {
	limits := make(map[string]int)
	for _, r := range runners {
		n := r.plan.Limits.MaxConcurrentCollections
		if n <= 0 {
			continue
		}
		if cur, ok := limits[r.plan.SourceID]; !ok || n < cur {
			limits[r.plan.SourceID] = n
		}
	}

	sems := make(map[string]chan struct{}, len(limits))
	for src, n := range limits {
		sems[src] = make(chan struct{}, n)
	}
	return sems
}
