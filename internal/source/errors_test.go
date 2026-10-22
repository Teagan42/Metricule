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
	if got := source.ReasonOf(err); got != telemetry.ReasonTimeout {
		t.Errorf("ReasonOf = %q, want %q", got, telemetry.ReasonTimeout)
	}
}

// The scheduler receives errors already wrapped with plan context, so the reason
// must survive wrapping.
func TestReasonOfTraversesWrapping(t *testing.T) {
	inner := queryError{err: errors.New("connection refused"), reason: telemetry.ReasonConnection}
	wrapped := fmt.Errorf("plan %q: %w", "app_jobs_pending", inner)

	if got := source.ReasonOf(wrapped); got != telemetry.ReasonConnection {
		t.Errorf("ReasonOf = %q, want %q", got, telemetry.ReasonConnection)
	}
}

// An unclassified error must not invent a reason.
func TestReasonOfFallsBackToUnknown(t *testing.T) {
	for name, err := range map[string]error{
		"plain":   errors.New("something went wrong"),
		"wrapped": fmt.Errorf("outer: %w", errors.New("inner")),
		"nil":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			if got := source.ReasonOf(err); got != telemetry.ReasonUnknown {
				t.Errorf("ReasonOf = %q, want %q", got, telemetry.ReasonUnknown)
			}
		})
	}
}

// Compile-time proof that the interface is satisfiable by an adapter error, which
// is the whole point of declaring it.
var _ source.Classified = queryError{}
