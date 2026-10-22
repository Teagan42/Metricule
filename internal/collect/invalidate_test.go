package collect_test

import (
	"context"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

func changeEvent(scope string) source.ChangeEvent {
	return source.ChangeEvent{SourceID: "app", Scope: scope, Kind: source.KindNotify, At: time.Now()}
}

func TestInvalidateTriggersARefreshOnTheTrailingEdge(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	plan := testPlan("jobs", time.Hour)
	plan.Debounce = 20 * time.Millisecond
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second) // startup

	sent := time.Now()
	sched.Invalidate(changeEvent(source.ScopeUnknown))
	coll.waitCalls(t, 1, 2*time.Second)
	elapsed := time.Since(sent)
	stop()

	if elapsed < 20*time.Millisecond {
		t.Fatalf("refresh ran %s after the event: a trailing debounce must wait the full window", elapsed)
	}
}

func TestEventBurstCoalescesIntoOneRefresh(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	plan := testPlan("jobs", time.Hour)
	plan.Debounce = 25 * time.Millisecond
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second) // startup

	for range 50 {
		sched.Invalidate(changeEvent(source.ScopeUnknown))
	}
	coll.waitCalls(t, 1, 2*time.Second)
	time.Sleep(60 * time.Millisecond)
	stop()

	if n := coll.count(); n != 2 {
		t.Fatalf("collector ran %d times, want 2: startup plus one coalesced refresh", n)
	}
	if got := familyTotal(t, reg, "metricule_collection_trigger_events_total"); got != 50 {
		t.Fatalf("trigger events = %v, want 50", got)
	}
	// The runner consumes some events directly and the rest fold into the pending
	// slot, so the exact split is timing-dependent; only the single refresh above is
	// an invariant.
	if got := familyTotal(t, reg, "metricule_collection_coalesced_events_total"); got == 0 {
		t.Fatal("coalesced events = 0, want the burst to fold into the pending refresh")
	}
}

func TestContinuousEventsCannotStarveARefresh(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	// A trailing debounce that is always reset would never fire without this cap.
	opts.MaxRefreshDelay = 30 * time.Millisecond
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	plan := testPlan("jobs", time.Hour)
	plan.Debounce = time.Second
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second) // startup

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-done:
				return
			default:
			}
			sched.Invalidate(changeEvent(source.ScopeUnknown))
			time.Sleep(time.Millisecond)
			if coll.count() > 1 {
				return
			}
		}
	}()

	coll.waitCalls(t, 1, 500*time.Millisecond)
	<-done
	stop()
}

func TestEventsDuringCollectionCauseASubsequentRefresh(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	inFlight := make(chan struct{}, 1)
	coll := newFakeCollector("jobs", func(_ context.Context, call int) (collect.Result, error) {
		if call == 1 {
			select {
			case inFlight <- struct{}{}:
			default:
			}
			time.Sleep(40 * time.Millisecond)
		}
		return okResult("jobs", float64(call)), nil
	})
	plan := testPlan("jobs", time.Hour)
	plan.Debounce = time.Millisecond
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	select {
	case <-inFlight:
	case <-time.After(2 * time.Second):
		t.Fatal("startup collection never began")
	}
	sched.Invalidate(changeEvent(source.ScopeUnknown))

	coll.waitCalls(t, 2, 2*time.Second)
	stop()

	if n := coll.count(); n < 2 {
		t.Fatalf("collector ran %d times: an event during collection must cause a later refresh", n)
	}
}

func TestInvalidateOnlyAffectsPlansWatchingTheScope(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	watching := succeedingCollector("jobs")
	jobs := testPlan("jobs", time.Hour)
	jobs.Debounce = time.Millisecond
	jobs.Watch = &source.WatchSpec{Tables: []string{"public.jobs"}}
	if err := sched.Add(jobs, watching); err != nil {
		t.Fatalf("Add jobs: %v", err)
	}

	other := succeedingCollector("users")
	users := testPlan("users", time.Hour)
	users.Debounce = time.Millisecond
	users.Watch = &source.WatchSpec{Tables: []string{"public.users"}}
	if err := sched.Add(users, other); err != nil {
		t.Fatalf("Add users: %v", err)
	}

	stop := startScheduler(t, sched)
	watching.waitCalls(t, 1, 2*time.Second)
	other.waitCalls(t, 1, 2*time.Second)

	sched.Invalidate(changeEvent("public.jobs"))
	watching.waitCalls(t, 1, 2*time.Second)
	time.Sleep(30 * time.Millisecond)
	stop()

	if n := other.count(); n != 1 {
		t.Fatalf("the users plan ran %d times, want 1: a scoped event must not invalidate it", n)
	}
}

func TestUnknownScopeInvalidatesEveryPlanOnTheSource(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	colls := map[string]*fakeCollector{}
	for _, id := range []string{"jobs", "users"} {
		c := succeedingCollector(id)
		colls[id] = c
		plan := testPlan(id, time.Hour)
		plan.Debounce = time.Millisecond
		plan.Watch = &source.WatchSpec{Tables: []string{"public." + id}}
		if err := sched.Add(plan, c); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	stop := startScheduler(t, sched)
	for _, c := range colls {
		c.waitCalls(t, 1, 2*time.Second)
	}

	sched.Invalidate(changeEvent(source.ScopeUnknown))
	for _, c := range colls {
		c.waitCalls(t, 1, 2*time.Second)
	}
	stop()
}

func TestInvalidateIgnoresUnknownSourcesAndNeverBlocks(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := sched.Add(testPlan("jobs", time.Hour), succeedingCollector("jobs")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Never started: a bounded channel must absorb or discard every event rather
	// than blocking the caller, which is the notification listener's goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.Invalidate(source.ChangeEvent{SourceID: "missing", Kind: source.KindNotify})
		for range 1000 {
			sched.Invalidate(changeEvent(source.ScopeUnknown))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Invalidate blocked")
	}
}

func TestReconcileRefreshesWithoutAnyChangeEvent(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	// Notify mode with no interval: only reconciliation can repair a lost
	// notification (AD-005).
	plan := testPlan("jobs", 0)
	plan.Mode = config.ModeNotify
	plan.Reconcile = 3 * time.Millisecond
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 4, 2*time.Second)
	stop()
}
