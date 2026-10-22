package collect

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// runner owns one plan. Collection runs inline on this single goroutine, which is
// what structurally prevents overlapping collection for the same plan (§7.2).
type runner struct {
	sched *Scheduler
	plan  Plan
	coll  Collector
	// sem caps concurrent collection across every plan on the same source. Assigned
	// by Run before any runner starts.
	sem chan struct{}
	// events holds at most one pending invalidation. A deeper buffer would only
	// schedule duplicate refreshes for the same unknown change (rule 10).
	events chan source.ChangeEvent

	// Readiness flags, read by Status from another goroutine.
	attempted atomic.Bool
	collected atomic.Bool
	// lastSuccess is the CollectedAt of the newest published snapshot, in Unix
	// nanoseconds, so failures can report a growing snapshot age.
	lastSuccess atomic.Int64
}

func newRunner(s *Scheduler, plan Plan, c Collector) *runner {
	return &runner{sched: s, plan: plan, coll: c, events: make(chan source.ChangeEvent, 1)}
}

// Pending deadlines for one plan. Owned by the runner goroutine, never shared.
//
// A single wake-up deadline replaces one timer per concern, so a plan costs one
// goroutine and one timer regardless of how many schedules apply to it.
type schedule struct {
	interval  time.Time
	reconcile time.Time
	retry     time.Time
	// attempt counts consecutive failures since the last success.
	attempt int

	// debounce is the trailing edge of the current invalidation burst, and maxDelay
	// is the hard deadline measured from the burst's first event.
	debounce time.Time
	maxDelay time.Time
	trigger  telemetry.Trigger
}

// Returns the earliest pending deadline and what it will trigger. A zero time means
// nothing is scheduled.
func (s *schedule) next() (time.Time, telemetry.Trigger) {
	var at time.Time
	var trigger telemetry.Trigger
	consider := func(t time.Time, tr telemetry.Trigger) {
		if t.IsZero() || (!at.IsZero() && !t.Before(at)) {
			return
		}
		at, trigger = t, tr
	}
	consider(s.interval, telemetry.TriggerSchedule)
	consider(s.reconcile, telemetry.TriggerReconcile)
	consider(s.retry, telemetry.TriggerSchedule)
	consider(s.debounce, s.trigger)
	consider(s.maxDelay, s.trigger)
	return at, trigger
}

// Folds a change event into the pending burst.
func (s *schedule) arm(now time.Time, ev source.ChangeEvent, window, maxDelay time.Duration) {
	if s.debounce.IsZero() {
		s.maxDelay = now.Add(maxDelay)
	}
	s.debounce = now.Add(window)
	s.trigger = triggerFor(ev.Kind)
}

// Clears the burst: a completed refresh covers every event observed so far.
func (s *schedule) disarm() {
	s.debounce = time.Time{}
	s.maxDelay = time.Time{}
}

func (r *runner) run(ctx context.Context) {
	var sc schedule
	// Startup collection so that no metric is absent merely because its interval has
	// not elapsed (§7.6).
	r.refreshAndReschedule(ctx, telemetry.TriggerStartup, &sc)

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		now := r.sched.now()
		due, trigger := sc.next()
		if !due.IsZero() && !due.After(now) {
			r.refreshAndReschedule(ctx, trigger, &sc)
			continue
		}

		// A nil channel parks the select when no schedule applies, which is the
		// steady state for a notify-only plan before its reconcile window.
		var wake <-chan time.Time
		if !due.IsZero() {
			timer.Reset(due.Sub(now))
			wake = timer.C
		}

		select {
		case <-ctx.Done():
			return
		case <-wake:
		case ev := <-r.events:
			sc.arm(r.sched.now(), ev, r.plan.Debounce, r.maxRefreshDelay())
		}
		timer.Stop()
	}
}

// Collects once and rearms every deadline from the outcome.
func (r *runner) refreshAndReschedule(ctx context.Context, trigger telemetry.Trigger, sc *schedule) {
	ok := r.refresh(ctx, trigger)
	now := r.sched.now()

	sc.disarm()
	sc.retry = time.Time{}
	if ok {
		// Success resets retry state so a recovered source returns to its normal
		// cadence immediately (§7.3).
		sc.attempt = 0
	} else {
		sc.attempt++
		if sc.attempt < r.sched.opts.Retry.MaxAttempts {
			sc.retry = now.Add(r.sched.opts.Retry.Delay(sc.attempt))
		} else {
			// Attempts are bounded: fall back to the plan's schedule rather than
			// retrying a dead source forever.
			sc.attempt = 0
		}
	}

	// Any refresh postpones the interval, so a notification-driven collection does
	// not leave a redundant poll queued behind it.
	if r.plan.Interval > 0 {
		sc.interval = now.Add(r.intervalDelay())
	}
	if r.plan.Reconcile > 0 {
		sc.reconcile = now.Add(r.plan.Reconcile)
	}
}

// Spreads interval schedules across the configured jitter window (§7.3).
func (r *runner) intervalDelay() time.Duration {
	if r.plan.Jitter <= 0 {
		return r.plan.Interval
	}
	return r.plan.Interval + time.Duration(r.sched.jitter()*float64(r.plan.Jitter))
}

// Executes one collection, publishing or failing according to the outcome.
func (r *runner) refresh(ctx context.Context, trigger telemetry.Trigger) bool {
	m := r.sched.opts.Metrics
	m.RefreshRequested(r.plan.ID, trigger)

	started := r.sched.now()
	res, err := r.collect(ctx)
	elapsed := r.sched.now().Sub(started)

	if err == nil {
		err = r.publish(res)
	}
	r.attempted.Store(true)
	if err != nil {
		r.recordFailure(elapsed, err)
		return false
	}

	m.CollectionSucceeded(r.plan.ID, r.plan.SourceID, elapsed,
		res.Snapshot.SampleCount(), res.Snapshot.CollectedAt)
	m.SetSnapshotAge(r.plan.ID, r.sched.now().Sub(res.Snapshot.CollectedAt))
	r.lastSuccess.Store(res.Snapshot.CollectedAt.UnixNano())
	r.collected.Store(true)
	return true
}

// Runs the collector under the plan's timeout, holding a source capacity slot.
//
// The timeout starts after acquisition so that waiting for capacity cannot consume
// the budget meant for query execution.
func (r *runner) collect(ctx context.Context) (Result, error) {
	if err := r.acquire(ctx); err != nil {
		return Result{}, err
	}
	defer r.release()

	if r.plan.Timeout <= 0 {
		return r.coll.Collect(ctx)
	}
	cctx, cancel := context.WithTimeout(ctx, r.plan.Timeout)
	defer cancel()
	return r.coll.Collect(cctx)
}

func (r *runner) acquire(ctx context.Context) error {
	if r.sem == nil {
		return nil
	}
	select {
	case r.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *runner) release() {
	if r.sem != nil {
		<-r.sem
	}
}

func (r *runner) publish(res Result) error {
	if res.Snapshot == nil {
		return ErrNoSnapshot
	}
	return r.sched.opts.Store.Publish(res.Snapshot)
}

// Records a failed attempt and applies the configured staleness policy.
func (r *runner) recordFailure(elapsed time.Duration, cause error) {
	reason := classify(cause)
	r.sched.opts.Metrics.CollectionFailed(r.plan.ID, r.plan.SourceID, elapsed, reason)
	r.sched.log.Error("collection failed",
		"collection", r.plan.ID, "source", r.plan.SourceID,
		"reason", string(reason), "error", cause)

	now := r.sched.now()
	if err := r.sched.opts.Store.Fail(r.plan.ID, now); err != nil {
		r.sched.log.Error("applying the staleness policy failed",
			"collection", r.plan.ID, "error", err)
	}

	// Republish the age so an operator can see retained data going stale rather than
	// a gauge frozen at the last success (P6, §8.4).
	if ns := r.lastSuccess.Load(); ns > 0 {
		r.sched.opts.Metrics.SetSnapshotAge(r.plan.ID, now.Sub(time.Unix(0, ns)))
	}
}
