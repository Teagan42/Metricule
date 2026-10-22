package collect_test

import (
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
)

func TestStartupRunsInitialCollection(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	// An hour-long interval proves the first collection came from startup (§7.6).
	if err := sched.Add(testPlan("jobs", time.Hour), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	stop()

	if n := coll.count(); n != 1 {
		t.Fatalf("collector ran %d times, want 1", n)
	}
	if got := store.Current(time.Now()); len(got) != 1 {
		t.Fatalf("store holds %d snapshots, want 1", len(got))
	}
}

func TestIntervalSchedulePollsRepeatedly(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	if err := sched.Add(testPlan("jobs", 2*time.Millisecond), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 4, 2*time.Second)
	stop()

	// Each poll republishes, so the last value tracks the call count.
	got := store.Current(time.Now())
	if len(got) != 1 {
		t.Fatalf("store holds %d snapshots, want 1", len(got))
	}
	if v := got[0].Families[0].Samples[0].Value; v < 4 {
		t.Fatalf("published value = %v, want at least 4 polls", v)
	}
}

func TestJitterStaysWithinTheConfiguredWindow(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	// A jitter source pinned to its maximum makes the delay deterministic.
	opts.Jitter = func() float64 { return 1 }
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	plan := testPlan("jobs", time.Millisecond)
	plan.Jitter = 30 * time.Millisecond
	coll := succeedingCollector("jobs")
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	start := time.Now()
	stop := startScheduler(t, sched)
	coll.waitCalls(t, 2, 2*time.Second)
	stop()

	// Startup is immediate; the second poll waits interval plus full jitter.
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("second poll ran after %s, want at least the 30ms jitter window", elapsed)
	}
}

func TestDuplicatePlanIDIsRejected(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := sched.Add(testPlan("jobs", time.Hour), succeedingCollector("jobs")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := sched.Add(testPlan("jobs", time.Hour), succeedingCollector("jobs")); err == nil {
		t.Fatal("Add accepted a duplicate plan ID")
	}
}
