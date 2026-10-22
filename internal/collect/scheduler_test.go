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
