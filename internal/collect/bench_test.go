package collect_test

import (
	"context"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

// publishingStore signals each successful publication.
type publishingStore struct {
	*snapshot.MemoryStore
	published chan struct{}
}

func (p *publishingStore) Publish(snap *snapshot.Snapshot) error {
	err := p.MemoryStore.Publish(snap)
	if err == nil {
		select {
		case p.published <- struct{}{}:
		default:
		}
	}
	return err
}

// publishingPipeline is a running notify plan whose publications are observable.
type publishingPipeline struct {
	sched     *collect.Scheduler
	published chan struct{}
	stop      func()
}

func newPublishingPipeline(tb testing.TB) *publishingPipeline {
	tb.Helper()
	store := &publishingStore{
		MemoryStore: snapshot.NewMemoryStore(snapshot.StoreConfig{
			Default: snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: time.Hour},
		}),
		published: make(chan struct{}, 1024),
	}
	sched, err := collect.NewScheduler(collect.Options{
		Store:   store,
		Metrics: telemetry.New(prometheus.NewRegistry(), version.Info{}),
		Logger:  slog.New(slog.DiscardHandler),
		Retry:   collect.RetryPolicy{MaxAttempts: 3, Base: time.Millisecond, Max: 4 * time.Millisecond, Factor: 2},
	})
	if err != nil {
		tb.Fatalf("NewScheduler: %v", err)
	}

	plan := testPlan("jobs", time.Hour)
	plan.Mode = config.ModeNotify
	plan.Debounce = 0
	if err := sched.Add(plan, succeedingCollector("jobs")); err != nil {
		tb.Fatalf("Add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			tb.Errorf("Run: %v", err)
		}
	}

	// Startup publication must not leak into measurements.
	select {
	case <-store.published:
	case <-time.After(5 * time.Second):
		stop()
		tb.Fatal("startup collection never published")
	}
	return &publishingPipeline{sched: sched, published: store.published, stop: stop}
}

func (p *publishingPipeline) awaitOne(tb testing.TB) {
	tb.Helper()
	select {
	case <-p.published:
	case <-time.After(5 * time.Second):
		tb.Fatal("notification did not reach publication")
	}
}

// Notification to published snapshot, including scheduling and the store write.
//
// A zero debounce keeps the measurement about the pipeline rather than a configured
// delay.
func BenchmarkNotificationToPublication(b *testing.B) {
	p := newPublishingPipeline(b)
	defer p.stop()

	b.ResetTimer()
	for range b.N {
		start := time.Now()
		p.sched.Invalidate(source.ChangeEvent{
			SourceID: "app", Scope: source.ScopeUnknown, Kind: source.KindNotify, At: start,
		})
		p.awaitOne(b)
		b.ReportMetric(float64(time.Since(start).Nanoseconds()), "ns/notification-to-publication")
	}
}

// Percentiles for the same pipeline. Benchmarks report an average; this reports the
// tail that operators actually feel (§20.6).
func TestNotificationToPublicationPercentiles(t *testing.T) {
	p := newPublishingPipeline(t)
	defer p.stop()

	const iterations = 500
	durations := make([]time.Duration, 0, iterations)
	for range iterations {
		start := time.Now()
		p.sched.Invalidate(source.ChangeEvent{
			SourceID: "app", Scope: source.ScopeUnknown, Kind: source.KindNotify, At: start,
		})
		p.awaitOne(t)
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	percentile := func(p float64) time.Duration {
		idx := int(float64(len(durations)) * p)
		if idx >= len(durations) {
			idx = len(durations) - 1
		}
		return durations[idx]
	}
	t.Logf("notification to publication: p50=%s p95=%s p99=%s max=%s",
		percentile(0.50), percentile(0.95), percentile(0.99), durations[len(durations)-1])
}

// Burst coalescing: a slow collection plus many notifications must not produce one
// publication per notification.
func BenchmarkBurstCoalescing(b *testing.B) {
	const burst = 1_000

	for range b.N {
		store := &publishingStore{
			MemoryStore: snapshot.NewMemoryStore(snapshot.StoreConfig{
				Default: snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: time.Hour},
			}),
			published: make(chan struct{}, burst+8),
		}
		sched, err := collect.NewScheduler(collect.Options{
			Store:   store,
			Metrics: telemetry.New(prometheus.NewRegistry(), version.Info{}),
			Logger:  slog.New(slog.DiscardHandler),
		})
		if err != nil {
			b.Fatal(err)
		}

		inFlight := make(chan struct{}, 1)
		coll := newFakeCollector("jobs", func(_ context.Context, call int) (collect.Result, error) {
			if call == 1 {
				select {
				case inFlight <- struct{}{}:
				default:
				}
				time.Sleep(20 * time.Millisecond)
			}
			return okResult("jobs", float64(call)), nil
		})
		plan := testPlan("jobs", time.Hour)
		plan.Mode = config.ModeNotify
		plan.Debounce = 5 * time.Millisecond
		if err := sched.Add(plan, coll); err != nil {
			b.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		select {
		case <-store.published:
		case <-time.After(5 * time.Second):
			b.Fatal("startup collection never published")
		}
		for range burst {
			sched.Invalidate(source.ChangeEvent{
				SourceID: "app", Scope: source.ScopeUnknown, Kind: source.KindNotify, At: time.Now(),
			})
		}
		// One follow-up publication is the maximum the coalescing contract allows
		// while the first collection is still running.
		select {
		case <-store.published:
		case <-time.After(5 * time.Second):
			b.Fatal("burst did not cause a follow-up refresh")
		}
		cancel()
		<-done

		publications := 2 // startup plus the follow-up consumed above
		for {
			select {
			case <-store.published:
				publications++
				continue
			default:
			}
			break
		}
		b.ReportMetric(float64(burst)/float64(publications-1), "notifications/publication")
	}
}
