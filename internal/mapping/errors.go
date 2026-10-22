package mapping

import (
	"errors"
	"fmt"

	"github.com/teagan42/metricule/internal/telemetry"
)

// Error reports a mapping failure with bounded diagnostic context (§12.3).
//
// Detail must stay free of source record data and secret values: a mapping error is
// logged, and a value that fails conversion may itself be sensitive.
type Error struct {
	Metric string
	Source string
	// Field is the mapping field that failed, such as "value" or "labels.status".
	Field  string
	Reason telemetry.Reason
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("metric %q (source %q): mapping field %q: %s: %s",
		e.Metric, e.Source, e.Field, e.Reason, e.Detail)
}

// ReasonOf extracts the telemetry reason from a mapping error.
//
// Returns ReasonUnknown for any other error, so a caller can always label a failure.
func ReasonOf(err error) telemetry.Reason {
	var me *Error
	if errors.As(err, &me) {
		return me.Reason
	}
	return telemetry.ReasonUnknown
}
