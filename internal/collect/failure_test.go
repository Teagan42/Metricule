package collect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
)

var errSourceDown = errors.New("source down")

func TestFailedCollectionRetriesUpToTheAttemptLimit(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	opts.Retry = collect.RetryPolicy{MaxAttempts: 3, Base: time.Millisecond, Max: 2 * time.Millisecond, Factor: 2}
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		return collect.Result{}, errSourceDown
	})
	// No interval: every collection after the first must come from a retry.
	plan := testPlan("jobs", 0)
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 3, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	stop()

	// Three attempts total, then the plan falls back to its (absent) schedule.
	if n := coll.count(); n != 3 {
		t.Fatalf("collector ran %d times, want exactly 3 bounded attempts", n)
	}
}

func TestSuccessResetsRetryState(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	opts.Retry = collect.RetryPolicy{MaxAttempts: 2, Base: time.Millisecond, Max: time.Millisecond, Factor: 2}
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// Fail, fail, then succeed forever. Without a reset the third failure would be
	// suppressed by the exhausted attempt budget.
	coll := newFakeCollector("jobs", func(_ context.Context, call int) (collect.Result, error) {
		if call <= 2 {
			return collect.Result{}, errSourceDown
		}
		return okResult("jobs", float64(call)), nil
	})
	if err := sched.Add(testPlan("jobs", time.Millisecond), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 5, 2*time.Second)
	stop()

	if got := store.Current(time.Now()); len(got) != 1 {
		t.Fatalf("store holds %d snapshots, want the recovered collection", len(got))
	}
}

func TestFailureAppliesStalenessPolicyWithoutFabricatingZeros(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Drop},
	})
	opts, _ := testOptions(t)
	opts.Store = store
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, call int) (collect.Result, error) {
		if call == 1 {
			return okResult("jobs", 42), nil
		}
		return collect.Result{}, errSourceDown
	})
	if err := sched.Add(testPlan("jobs", time.Millisecond), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 2, 2*time.Second)
	stop()

	for _, snap := range store.Current(time.Now()) {
		for _, f := range snap.Families {
			for _, s := range f.Samples {
				if s.Value == 0 {
					t.Fatalf("failure produced a zero sample in %s", f.Name)
				}
			}
		}
	}
}

func TestCollectorSuccessWithoutSnapshotIsTreatedAsFailure(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		return collect.Result{PlanID: "jobs"}, nil
	})
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	stop()

	if got := store.Current(time.Now()); len(got) != 0 {
		t.Fatalf("store holds %d snapshots, want 0", len(got))
	}
}

func TestRejectedPublicationIsRecordedAsAFailedCollection(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// A duplicate labelset must fail the collection, not publish half of it (§8.3).
	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		res := okResult("jobs", 1)
		res.Snapshot.Families[0].Samples = []snapshot.Sample{{Value: 1}, {Value: 2}}
		return res, nil
	})
	plan := testPlan("jobs", 0)
	plan.Failure = config.Failure{Mode: config.FailureDrop}
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	stop()

	if got := store.Current(time.Now()); len(got) != 0 {
		t.Fatalf("store holds %d snapshots, want 0", len(got))
	}
}

func TestRetryDelayGrowsAndIsCapped(t *testing.T) {
	t.Parallel()

	p := collect.RetryPolicy{MaxAttempts: 10, Base: time.Second, Max: 4 * time.Second, Factor: 2}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	for i, w := range want {
		if got := p.Delay(i + 1); got != w {
			t.Fatalf("Delay(%d) = %s, want %s", i+1, got, w)
		}
	}
}
