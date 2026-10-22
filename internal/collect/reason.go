package collect

import (
	"context"
	"errors"

	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
)

// ErrNoSnapshot rejects a collector that reports success without a result. The
// Collector contract requires an error instead of a partial Result.
var ErrNoSnapshot = errors.New("collector returned no snapshot without an error")

// Maps a collection error onto a bounded telemetry reason.
//
// Only sentinels visible across package boundaries are classified; an adapter's own
// taxonomy stays in its logs because the scheduler holds no adapter logic (P7).
func classify(err error) telemetry.Reason {
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
