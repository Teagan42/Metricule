package collect_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

// Builds scheduler options backed by a real store and a private registry.
func testOptions(t *testing.T) (collect.Options, *snapshot.MemoryStore) {
	t.Helper()
	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: time.Hour},
	})
	return collect.Options{
		Store:   store,
		Metrics: telemetry.New(prometheus.NewRegistry(), version.Info{}),
		Logger:  slog.New(slog.DiscardHandler),
		Retry: collect.RetryPolicy{
			MaxAttempts: 3,
			Base:        time.Millisecond,
			Max:         4 * time.Millisecond,
			Factor:      2,
		},
	}, store
}

// Builds a poll plan with test-scale timings.
func testPlan(id string, interval time.Duration) collect.Plan {
	return collect.Plan{
		ID:       id,
		SourceID: "app",
		Mode:     config.ModePoll,
		Interval: interval,
		Timeout:  2 * time.Second,
		Debounce: 5 * time.Millisecond,
		Limits:   config.Limits{MaxConcurrentCollections: 4},
		Failure:  config.Failure{Mode: config.FailureRetain, MaxStaleAge: config.Duration(time.Hour)},
	}
}

// Records calls and defers behavior to fn, which sees a 1-based call number.
type fakeCollector struct {
	id   string
	fn   func(ctx context.Context, call int) (collect.Result, error)
	mu   sync.Mutex
	n    int
	live int
	// maxLive is the highest observed overlap, used to prove serialization.
	maxLive int
	calls   chan int
}

func newFakeCollector(id string, fn func(ctx context.Context, call int) (collect.Result, error)) *fakeCollector {
	return &fakeCollector{id: id, fn: fn, calls: make(chan int, 1024)}
}

// Builds a collector that always succeeds with a one-series snapshot.
func succeedingCollector(id string) *fakeCollector {
	return newFakeCollector(id, func(_ context.Context, call int) (collect.Result, error) {
		return okResult(id, float64(call)), nil
	})
}

func (f *fakeCollector) ID() string { return f.id }

func (f *fakeCollector) Collect(ctx context.Context) (collect.Result, error) {
	f.mu.Lock()
	f.n++
	call := f.n
	f.live++
	if f.live > f.maxLive {
		f.maxLive = f.live
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.live--
		f.mu.Unlock()
		select {
		case f.calls <- call:
		default:
		}
	}()

	return f.fn(ctx, call)
}

func (f *fakeCollector) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeCollector) peakConcurrency() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxLive
}

// Blocks until n collections have completed or the deadline expires.
func (f *fakeCollector) waitCalls(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for range n {
		select {
		case <-f.calls:
		case <-deadline:
			t.Fatalf("collector %q: only %d of %d collections ran within %s", f.id, f.count(), n, within)
		}
	}
}

func okResult(id string, value float64) collect.Result {
	return collect.Result{
		PlanID:   id,
		SourceID: "app",
		RowCount: 1,
		Snapshot: &snapshot.Snapshot{
			CollectionID: id,
			SourceID:     "app",
			CollectedAt:  time.Now(),
			Families: []snapshot.Family{{
				Name:    "app_" + id,
				Help:    "Test family.",
				Type:    snapshot.TypeGauge,
				Samples: []snapshot.Sample{{Value: value}},
			}},
		},
	}
}

// Starts the scheduler and returns a stop function that waits for a clean exit.
func startScheduler(t *testing.T, s *collect.Scheduler) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	return func() {
		t.Helper()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v, want nil on cancellation", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return within 5s of cancellation")
		}
	}
}
