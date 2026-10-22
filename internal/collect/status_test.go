package collect_test

import (
	"context"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
)

func TestStatusIsNotReadyBeforeTheInitialCollection(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := sched.Add(testPlan("jobs", time.Hour), succeedingCollector("jobs")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	st := sched.Status()
	if st.Plans != 1 || st.Collected != 0 || st.Attempted != 0 {
		t.Fatalf("Status = %+v, want 1 plan with nothing attempted", st)
	}
	if st.Ready() {
		t.Fatal("Status reports ready before the initial collection (§7.6)")
	}
}

func TestStatusBecomesReadyAfterTheInitialCollection(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	coll := succeedingCollector("jobs")
	if err := sched.Add(testPlan("jobs", time.Hour), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	// waitCalls returns as the collector returns, before the publish completes.
	waitFor(t, time.Second, func() bool { return sched.Status().Ready() })
	stop()
}

func TestStatusDistinguishesFailingFromUncollected(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		return collect.Result{}, errSourceDown
	})
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	waitFor(t, time.Second, func() bool { return sched.Status().Attempted == 1 })
	stop()

	st := sched.Status()
	if st.Collected != 0 {
		t.Fatalf("Status.Collected = %d, want 0: no collection ever succeeded", st.Collected)
	}
	if st.Ready() {
		t.Fatal("Status reports ready while every collection fails")
	}
}

func TestStoreConfigCarriesEachPlanFailurePolicy(t *testing.T) {
	t.Parallel()

	plans := []collect.Plan{
		{
			ID:      "retained",
			Limits:  config.Limits{MaxTotalSeries: 500},
			Failure: config.Failure{Mode: config.FailureRetain, MaxStaleAge: config.Duration(2 * time.Minute)},
		},
		{
			ID:      "dropped",
			Limits:  config.Limits{MaxTotalSeries: 500},
			Failure: config.Failure{Mode: config.FailureDrop},
		},
	}

	cfg := collect.StoreConfigFor(plans)
	if cfg.MaxTotalSeries != 500 {
		t.Fatalf("MaxTotalSeries = %d, want 500", cfg.MaxTotalSeries)
	}
	if got := cfg.Policies["retained"]; got.Mode != snapshot.Retain || got.MaxStaleAge != 2*time.Minute {
		t.Fatalf("retained policy = %+v, want retain for 2m", got)
	}
	if got := cfg.Policies["dropped"]; got.Mode != snapshot.Drop {
		t.Fatalf("dropped policy = %+v, want drop", got)
	}
}

func TestStoreConfigTakesTheStrictestTotalSeriesLimit(t *testing.T) {
	t.Parallel()

	plans := []collect.Plan{
		{ID: "a", Limits: config.Limits{MaxTotalSeries: 1000}},
		{ID: "b", Limits: config.Limits{MaxTotalSeries: 250}},
	}
	if got := collect.StoreConfigFor(plans).MaxTotalSeries; got != 250 {
		t.Fatalf("MaxTotalSeries = %d, want the strictest limit 250", got)
	}
}

// Polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition did not hold within %s", within)
}
