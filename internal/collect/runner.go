package collect

import (
	"context"
	"time"

	"github.com/teagan42/metricule/internal/telemetry"
)

// runner owns one plan. Collection runs inline on this single goroutine, which is
// what structurally prevents overlapping collection for the same plan (§7.2).
type runner struct {
	sched *Scheduler
	plan  Plan
	coll  Collector
}

func newRunner(s *Scheduler, plan Plan, c Collector) *runner {
	return &runner{sched: s, plan: plan, coll: c}
}

func (r *runner) run(ctx context.Context) {
	// Startup collection so that no metric is absent merely because its interval has
	// not elapsed (§7.6).
	r.refresh(ctx, telemetry.TriggerStartup)

	<-ctx.Done()
}

// Executes one collection and publishes or fails according to the outcome.
func (r *runner) refresh(ctx context.Context, trigger telemetry.Trigger) {
	m := r.sched.opts.Metrics
	m.RefreshRequested(r.plan.ID, trigger)

	started := r.sched.now()
	res, err := r.collect(ctx)
	elapsed := r.sched.now().Sub(started)

	if err == nil {
		err = r.publish(res)
	}
	if err != nil {
		r.recordFailure(elapsed, err)
		return
	}

	m.CollectionSucceeded(r.plan.ID, r.plan.SourceID, elapsed, res.Snapshot.SampleCount(), res.Snapshot.CollectedAt)
	m.SetSnapshotAge(r.plan.ID, r.sched.now().Sub(res.Snapshot.CollectedAt))
}

// Runs the collector under the plan's timeout.
func (r *runner) collect(ctx context.Context) (Result, error) {
	if r.plan.Timeout <= 0 {
		return r.coll.Collect(ctx)
	}
	cctx, cancel := context.WithTimeout(ctx, r.plan.Timeout)
	defer cancel()
	return r.coll.Collect(cctx)
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

	if err := r.sched.opts.Store.Fail(r.plan.ID, r.sched.now()); err != nil {
		r.sched.log.Error("applying the staleness policy failed",
			"collection", r.plan.ID, "error", err)
	}
}
