package collect_test

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/telemetry"
)

func TestRetriesAreAttributedToTheRetryTrigger(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	opts.Retry = collect.RetryPolicy{MaxAttempts: 3, Base: time.Millisecond, Max: 2 * time.Millisecond, Factor: 2}
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		return collect.Result{}, errSourceDown
	})
	// No interval: the first attempt is startup, every later one is a retry.
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 3, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return refreshesWithTrigger(t, reg, telemetry.TriggerRetry) == 2
	})
	stop()

	// Three attempts: one startup, two retries. A counter that over-reports retries
	// is as misleading as one that under-reports them.
	if got := refreshesWithTrigger(t, reg, telemetry.TriggerRetry); got != 2 {
		t.Fatalf("retry refreshes = %v, want 2", got)
	}
	if got := refreshesWithTrigger(t, reg, telemetry.TriggerStartup); got != 1 {
		t.Fatalf("startup refreshes = %v, want 1: the first attempt is not a retry", got)
	}
}

func TestFirstAttemptsAreNeverAttributedToRetry(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := succeedingCollector("jobs")
	if err := sched.Add(testPlan("jobs", 2*time.Millisecond), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 5, 2*time.Second)
	stop()

	if got := refreshesWithTrigger(t, reg, telemetry.TriggerRetry); got != 0 {
		t.Fatalf("retry refreshes = %v, want 0: nothing failed", got)
	}
	if got := refreshesWithTrigger(t, reg, telemetry.TriggerSchedule); got == 0 {
		t.Fatal("scheduled polls were not attributed to the schedule trigger")
	}
}

// Guards a wrong number, not a wrong label: a retry inheriting notify made one
// notification look like two, inflating apparent write volume exactly while someone
// was diagnosing why collections failed.
func TestRetryAfterANotificationIsAttributedToRetryNotNotify(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	opts.Retry = collect.RetryPolicy{MaxAttempts: 2, Base: time.Millisecond, Max: time.Millisecond, Factor: 2}
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// Startup succeeds so only the notification-driven attempt fails and retries.
	coll := newFakeCollector("jobs", func(_ context.Context, call int) (collect.Result, error) {
		if call == 1 {
			return okResult("jobs", 1), nil
		}
		return collect.Result{}, errSourceDown
	})
	plan := testPlan("jobs", time.Hour)
	plan.Debounce = time.Millisecond
	if err := sched.Add(plan, coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	sched.Invalidate(changeEvent(""))
	coll.waitCalls(t, 2, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return refreshesWithTrigger(t, reg, telemetry.TriggerRetry) == 1
	})
	stop()

	// One notification, one notify refresh. Its re-attempt counts as a retry.
	if got := refreshesWithTrigger(t, reg, telemetry.TriggerNotify); got != 1 {
		t.Fatalf("notify refreshes = %v, want 1", got)
	}
	if got := refreshesWithTrigger(t, reg, telemetry.TriggerRetry); got != 1 {
		t.Fatalf("retry refreshes = %v, want 1", got)
	}
}

// Sums collection_refresh_total for one trigger.
func refreshesWithTrigger(t *testing.T, reg *prometheus.Registry, trigger telemetry.Trigger) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	total := 0.0
	for _, f := range families {
		if f.GetName() != "metricule_collection_refresh_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "trigger" && l.GetValue() == string(trigger) {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}
