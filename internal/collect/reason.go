package collect

import (
	"context"
	"errors"

	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// ErrNoSnapshot rejects a collector that reports success without a result. The
// Collector contract requires an error instead of a partial Result.
var ErrNoSnapshot = errors.New("collector returned no snapshot without an error")

// Reasons an adapter may report for itself. Any other value is discarded, so a
// malformed or attacker-influenced error string can never become a label (rule 7).
//
// Cardinality and duplicate_series are deliberately absent: those are publication
// verdicts the snapshot store owns, and cardinality additionally drives the rejection
// counter. An adapter must not be able to claim either.
var adapterReasons = map[telemetry.Reason]bool{
	telemetry.ReasonTimeout:        true,
	telemetry.ReasonConnection:     true,
	telemetry.ReasonQuery:          true,
	telemetry.ReasonDecode:         true,
	telemetry.ReasonConversion:     true,
	telemetry.ReasonNullValue:      true,
	telemetry.ReasonMissingField:   true,
	telemetry.ReasonResultTooLarge: true,
	telemetry.ReasonUnavailable:    true,
}

// Maps a collection error onto a bounded telemetry reason.
//
// An adapter classifies its own failures through source.Classified, so the scheduler
// holds no adapter-specific logic and imports no adapter package (P7). Everything
// else falls back to sentinels this package can see.
func classify(err error) telemetry.Reason {
	if r, ok := adapterReason(err); ok {
		return r
	}

	switch {
	case err == nil:
		return telemetry.ReasonUnknown
	case errors.Is(err, context.DeadlineExceeded):
		return telemetry.ReasonTimeout
	case errors.Is(err, context.Canceled):
		return telemetry.ReasonUnavailable
	case errors.Is(err, snapshot.ErrTotalSeriesLimit):
		return telemetry.ReasonCardinality
	case errors.Is(err, snapshot.ErrDuplicateSeries):
		return telemetry.ReasonDuplicateSeries
	case errors.Is(err, snapshot.ErrInvalidSample),
		errors.Is(err, snapshot.ErrUnsortedLabels),
		errors.Is(err, snapshot.ErrDuplicateFamily),
		errors.Is(err, snapshot.ErrNoCollectionID):
		return telemetry.ReasonConversion
	default:
		return telemetry.ReasonUnknown
	}
}

// Extracts a self-reported reason from an error chain, rejecting values outside the
// closed set.
//
// The second return must stay distinct from an "unknown" reason. Collapsing the two
// would make an unclassified error indistinguishable from one an adapter classified
// as unknown, and the sentinel chain in classify would never run: a timeout or a
// publication rejection would report "unknown" instead of its real cause.
func adapterReason(err error) (telemetry.Reason, bool) {
	r, ok := source.ReasonOf(err)
	if !ok || !adapterReasons[r] {
		return "", false
	}
	return r, true
}
