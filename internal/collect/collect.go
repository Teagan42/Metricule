// Package collect defines the collection contract.
//
// This file declares the contract only. Agent 2 implements the scheduler, whose
// requirements are in §7: per-plan schedules, coalescing, debouncing, bounded
// retry, per-source concurrency limits, and mandatory periodic reconciliation.
package collect

import (
	"context"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

// Plan is a compiled unit of collection work.
//
// Metrics that share an identical source query may compile into one plan so the
// query executes once (§7.3). A plan therefore owns one or more metric families.
type Plan struct {
	// ID is a stable, bounded identifier used as the `collection` telemetry label.
	ID       string
	SourceID string

	// Spec is the adapter-compiled query. Opaque to the scheduler (P7).
	Spec source.QuerySpec

	// Metrics are the families this plan publishes.
	Metrics []config.Metric

	Mode     config.CollectMode
	Interval time.Duration
	Timeout  time.Duration
	Debounce time.Duration
	Jitter   time.Duration
	// Reconcile forces a refresh even when no change signal arrives, repairing lost
	// notifications (AD-005).
	Reconcile time.Duration

	// Watch is nil for poll-only plans.
	Watch *source.WatchSpec

	Limits  config.Limits
	Failure config.Failure
}

// Result is the outcome of one collection attempt.
//
// Named Result rather than §6's CollectionResult to avoid stuttering as
// collect.Result; responsibilities are unchanged.
type Result struct {
	PlanID   string
	SourceID string

	// Snapshot is the mapped, validated output. Nil when Err is set.
	Snapshot *snapshot.Snapshot

	StartedAt time.Time
	Duration  time.Duration

	// RowCount is the raw row count before mapping, for diagnostics.
	RowCount int
}

// Collector executes one plan.
//
// Implementations must honor context cancellation and must not retain partial
// results on failure.
type Collector interface {
	// ID returns the plan identifier.
	ID() string

	// Collect executes the plan once. Returns an error rather than a partial
	// Result, so a caller can never publish half a collection.
	Collect(ctx context.Context) (Result, error)
}
