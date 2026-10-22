package source_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// A representative adapter error.
type queryError struct {
	err    error
	reason telemetry.Reason
}

func (e queryError) Error() string            { return "query: " + e.err.Error() }
func (e queryError) Unwrap() error            { return e.err }
func (e queryError) Reason() telemetry.Reason { return e.reason }

func TestReasonOfReadsAdapterReason(t *testing.T) {
	err := queryError{err: errors.New("context deadline exceeded"), reason: telemetry.ReasonTimeout}
	got, ok := source.ReasonOf(err)
	if !ok {
		t.Fatal("ReasonOf did not report a classified error")
	}
	if got != telemetry.ReasonTimeout {
		t.Errorf("ReasonOf = %q, want %q", got, telemetry.ReasonTimeout)
	}
}

// The scheduler receives errors already wrapped with plan context, so the reason
// must survive wrapping.
func TestReasonOfTraversesWrapping(t *testing.T) {
	inner := queryError{err: errors.New("connection refused"), reason: telemetry.ReasonConnection}
	wrapped := fmt.Errorf("plan %q: %w", "app_jobs_pending", inner)

	got, ok := source.ReasonOf(wrapped)
	if !ok {
		t.Fatal("ReasonOf lost the classification through wrapping")
	}
	if got != telemetry.ReasonConnection {
		t.Errorf("ReasonOf = %q, want %q", got, telemetry.ReasonConnection)
	}
}

// An unclassified error must not invent a reason, and must say it was unclassified.
//
// The boolean is what lets a caller with its own sentinel chain tell "nobody
// reported" from "an adapter reported unknown"; collapsing both would silently
// swallow that caller's fallback handling.
func TestReasonOfReportsUnclassified(t *testing.T) {
	for name, err := range map[string]error{
		"plain":   errors.New("something went wrong"),
		"wrapped": fmt.Errorf("outer: %w", errors.New("inner")),
		"nil":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := source.ReasonOf(err)
			if ok {
				t.Errorf("ReasonOf reported a classification for an unclassified error")
			}
			if got != telemetry.ReasonUnknown {
				t.Errorf("ReasonOf = %q, want %q", got, telemetry.ReasonUnknown)
			}
		})
	}
}

// An adapter that explicitly reports Unknown is distinguishable from one that
// reported nothing. Without this the two collapse and a caller cannot tell whether
// to consult its own sentinels.
func TestExplicitUnknownIsDistinctFromUnclassified(t *testing.T) {
	reported, ok := source.ReasonOf(queryError{err: errors.New("boom"), reason: telemetry.ReasonUnknown})
	if !ok {
		t.Error("an adapter reporting Unknown must still count as classified")
	}
	if reported != telemetry.ReasonUnknown {
		t.Errorf("reason = %q, want %q", reported, telemetry.ReasonUnknown)
	}

	if _, ok := source.ReasonOf(errors.New("boom")); ok {
		t.Error("an unclassified error must not count as classified")
	}
}

// Compile-time proof that the interface is satisfiable by an adapter error, which
// is the whole point of declaring it.
var _ source.Classified = queryError{}
