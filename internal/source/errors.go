package source

import (
	"errors"

	"github.com/teagan42/metricule/internal/telemetry"
)

// Classified is implemented by adapter errors that can name their own failure
// reason.
//
// Exists so the scheduler can label failure telemetry without type-asserting
// concrete adapter types (P7) and without an adapter importing internal/collect
// (rule 5). An adapter wraps its driver errors in a type implementing this
// interface; consumers reach it with errors.As.
//
// Consumers must still bound the label space: a Reason outside the set they accept
// collapses to telemetry.ReasonUnknown rather than widening the label dimension
// (§13).
type Classified interface {
	error

	// Reason returns the bounded label for this failure.
	Reason() telemetry.Reason
}

// ReasonOf extracts an adapter-reported reason, falling back to ReasonUnknown.
//
// Does not validate the value against a caller's allowlist; apply that separately
// so each consumer controls its own label space.
func ReasonOf(err error) telemetry.Reason {
	var c Classified
	if errors.As(err, &c) {
		return c.Reason()
	}
	return telemetry.ReasonUnknown
}

// Diagnosable is implemented by sources that expose runtime counters.
//
// Optional. Adapters deliberately receive no *telemetry.Metrics — Settings carries
// no telemetry handle — so the component running the health loop reads these and
// publishes the source-level families itself.
type Diagnosable interface {
	Diagnostics() Diagnostics
}

// Diagnostics are adapter-reported runtime counters.
//
// Counters are monotonic totals for the life of the source. A publisher tracks the
// previous value and adds the delta, so a restart cannot make a counter go backwards.
type Diagnostics struct {
	// Reconnects totals successful reconnections. Each one forces reconciliation,
	// because delivery during a disconnect is not durable (AD-005).
	Reconnects uint64
}
