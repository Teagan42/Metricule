package collect_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
)

func TestPerSourceConcurrencyIsBounded(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// Four plans on one source, capacity two. A tracker shared across collectors
	// records the peak overlap.
	var live, peak atomic.Int64
	colls := make([]*fakeCollector, 4)
	for i := range colls {
		id := "plan" + string(rune('a'+i))
		colls[i] = newFakeCollector(id, func(_ context.Context, call int) (collect.Result, error) {
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			live.Add(-1)
			return okResult(id, float64(call)), nil
		})

		plan := testPlan(id, 5*time.Millisecond)
		plan.Limits = config.Limits{MaxConcurrentCollections: 2}
		if err := sched.Add(plan, colls[i]); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	stop := startScheduler(t, sched)
	for _, c := range colls {
		c.waitCalls(t, 3, 5*time.Second)
	}
	stop()

	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrency on the source was %d, want at most 2", p)
	}
	if p := peak.Load(); p < 2 {
		t.Fatalf("peak concurrency was %d: the limit appears to serialize the source entirely", p)
	}
}

func TestStricterPlanLimitConstrainsTheWholeSource(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	var live, peak atomic.Int64
	colls := make([]*fakeCollector, 3)
	for i := range colls {
		id := "plan" + string(rune('a'+i))
		colls[i] = newFakeCollector(id, func(_ context.Context, call int) (collect.Result, error) {
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			live.Add(-1)
			return okResult(id, float64(call)), nil
		})

		plan := testPlan(id, 2*time.Millisecond)
		limit := 8
		if i == 0 {
			limit = 1
		}
		plan.Limits = config.Limits{MaxConcurrentCollections: limit}
		if err := sched.Add(plan, colls[i]); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	stop := startScheduler(t, sched)
	for _, c := range colls {
		c.waitCalls(t, 3, 5*time.Second)
	}
	stop()

	if p := peak.Load(); p != 1 {
		t.Fatalf("peak concurrency was %d, want 1: the strictest plan limit bounds the source", p)
	}
}

func TestPlanNeverCollectsConcurrentlyWithItself(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, call int) (collect.Result, error) {
		time.Sleep(8 * time.Millisecond)
		return okResult("jobs", float64(call)), nil
	})
	// An interval far shorter than the collection would overlap a naive scheduler.
	if err := sched.Add(testPlan("jobs", time.Millisecond), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 5, 5*time.Second)
	stop()

	if p := coll.peakConcurrency(); p != 1 {
		t.Fatalf("plan collected %d times concurrently with itself, want 1", p)
	}
}

func TestCollectionTimeoutCancelsTheCollectorContext(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	observed := make(chan error, 4)
	coll := newFakeCollector("jobs", func(ctx context.Context, _ int) (collect.Result, error) {
		<-ctx.Done()
		select {
		case observed <- ctx.Err():
		default:
		}
		return collect.Result{}, ctx.Err()
	})
	plan := testPlan("jobs", 0)
	plan.Timeout = 10 * time.Millisecond
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	select {
	case err := <-observed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("collector context error = %v, want DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collector context was never cancelled by the plan timeout")
	}
	stop()

	if got := store.Current(time.Now()); len(got) != 0 {
		t.Fatalf("a timed-out collection published %d snapshots, want 0", len(got))
	}
}

func TestShutdownCancelsAnInFlightCollection(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	entered := make(chan struct{}, 1)
	coll := newFakeCollector("jobs", func(ctx context.Context, _ int) (collect.Result, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return collect.Result{}, ctx.Err()
	})
	plan := testPlan("jobs", 0)
	plan.Timeout = time.Hour
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("collection never started")
	}
	// stop() fails the test if Run does not unwind the in-flight collection.
	stop()
}
