package collect_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// Mimics an adapter error that classifies itself, as the PostgreSQL adapter does.
type reasonedError struct {
	reason telemetry.Reason
	err    error
}

func (e reasonedError) Error() string            { return e.err.Error() }
func (e reasonedError) Unwrap() error            { return e.err }
func (e reasonedError) Reason() telemetry.Reason { return e.reason }

// Pins the declared contract. An adapter error that drifts from source.Classified
// stops compiling rather than silently degrading to "unknown".
var _ source.Classified = reasonedError{}

func TestAdapterReportedReasonReachesTelemetry(t *testing.T) {
	t.Parallel()

	cases := []telemetry.Reason{
		telemetry.ReasonConnection,
		telemetry.ReasonQuery,
		telemetry.ReasonDecode,
		telemetry.ReasonResultTooLarge,
		telemetry.ReasonUnavailable,
	}

	for _, want := range cases {
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()
			opts, _, reg := testOptionsWithRegistry(t)
			sched, err := collect.NewScheduler(opts)
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}

			// Wrapped, because an adapter reports through its own error chain.
			cause := fmt.Errorf("query jobs: %w",
				reasonedError{reason: want, err: errSourceDown})
			coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
				return collect.Result{}, cause
			})
			if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
				t.Fatalf("Add: %v", err)
			}

			stop := startScheduler(t, sched)
			coll.waitCalls(t, 1, 2*time.Second)
			waitFor(t, time.Second, func() bool {
				return errorsWithReason(t, reg, string(want)) > 0
			})
			stop()
		})
	}
}

func TestUnrecognizedAdapterReasonFallsBackToUnknown(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// An adapter must not be able to turn arbitrary text into a label value (rule 7).
	cause := reasonedError{reason: telemetry.Reason("pq: password for user bob"), err: errSourceDown}
	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		return collect.Result{}, cause
	})
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return errorsWithReason(t, reg, string(telemetry.ReasonUnknown)) > 0
	})
	stop()

	if got := errorsWithReason(t, reg, "pq: password for user bob"); got != 0 {
		t.Fatalf("an unvalidated reason became a label value")
	}
}

func TestPublicationRejectionsKeepTheirOwnReasons(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want telemetry.Reason
	}{
		{"timeout", context.DeadlineExceeded, telemetry.ReasonTimeout},
		{"cardinality", snapshot.ErrTotalSeriesLimit, telemetry.ReasonCardinality},
		{"duplicate", snapshot.ErrDuplicateSeries, telemetry.ReasonDuplicateSeries},
		{"invalid sample", snapshot.ErrInvalidSample, telemetry.ReasonConversion},
		{"unclassified", errSourceDown, telemetry.ReasonUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts, _, reg := testOptionsWithRegistry(t)
			sched, err := collect.NewScheduler(opts)
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			cause := fmt.Errorf("collect jobs: %w", tc.err)
			coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
				return collect.Result{}, cause
			})
			if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
				t.Fatalf("Add: %v", err)
			}

			stop := startScheduler(t, sched)
			coll.waitCalls(t, 1, 2*time.Second)
			waitFor(t, time.Second, func() bool {
				return errorsWithReason(t, reg, string(tc.want)) > 0
			})
			stop()
		})
	}
}

// Sums collection_errors_total for one reason label.
func errorsWithReason(t *testing.T, reg *prometheus.Registry, reason string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	total := 0.0
	for _, f := range families {
		if f.GetName() != "metricule_collection_errors_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == reason {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}
