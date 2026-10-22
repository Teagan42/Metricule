package source

import (
	"errors"
	"time"

	"github.com/teagan42/metricule/internal/telemetry/reason"
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
// collapses to reason.Unknown rather than widening the label dimension (§13).
type Classified interface {
	error

	// Reason returns the bounded label for this failure.
	Reason() reason.Reason
}

// ReasonOf extracts an adapter-reported reason.
//
// The second return reports whether an adapter actually classified the error, which
// a caller with its own fallback chain needs: collapsing an unclassified error to
// Unknown would swallow a caller's sentinel handling, so "nobody reported" stays
// distinct from "an adapter reported unknown".
//
// Does not validate the value against a caller's allowlist; apply that separately so
// each consumer controls its own label space.
//
//	if r, ok := source.ReasonOf(err); ok { ... }
func ReasonOf(err error) (reason.Reason, bool) {
	var c Classified
	if errors.As(err, &c) {
		return c.Reason(), true
	}
	return reason.Unknown, false
}

// Diagnosable is implemented by sources that expose their health state.
//
// Optional. Adapters deliberately receive no telemetry handle — Settings carries
// none — so whoever runs the health loop reads this and publishes the source-level
// families itself.
type Diagnosable interface {
	Diagnostics() Diagnostics
}

// Diagnostics is an adapter's self-reported health state.
//
// Uniform across adapters so `/readyz` and `metricule test` can show per-source
// detail without knowing concrete types (§13.1, §15.2). Every field is something
// any source has: reachability, a last error, a failure streak, and a loggable
// target. A field an adapter cannot populate stays zero.
//
// Target and LastError MUST be safe to log and display. Credentials are resolved
// before an adapter is constructed, so an adapter must redact them here (§14.3).
type Diagnostics struct {
	// Target identifies the source for operators: a credential-free
	// "host:port/database", a file path, or a base URL.
	Target string

	// Up reports reachability at CheckedAt.
	Up bool

	// CheckedAt is the last health check. Zero until the first check.
	CheckedAt time.Time

	// LastSuccess is the last successful check. Zero until the first success.
	// Distinguishes "never reachable" from "was reachable, now is not" (P6).
	LastSuccess time.Time

	// ConsecutiveFailures resets on any successful check.
	ConsecutiveFailures int

	// Reconnects totals successful reconnections and is monotonic for the life of
	// the source: not reset by Close, nor by a later Open. A publisher derives a
	// counter delta from it, so resetting it would make the counter move backwards.
	// Each reconnection forces reconciliation, because delivery during a
	// disconnect is not durable (AD-005).
	Reconnects uint64

	// LastError is retained after recovery, so a transient failure remains
	// diagnosable once the source is healthy again.
	LastError error

	// Reason classifies LastError.
	Reason reason.Reason
}
