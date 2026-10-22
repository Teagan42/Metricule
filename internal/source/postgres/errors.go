package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/teagan42/metricule/internal/telemetry"
)

// ErrResultTooLarge reports a result that exceeds max_rows_per_query.
//
// Returned instead of a truncated ResultSet: §8.7 prohibits silent truncation.
var ErrResultTooLarge = errors.New("postgres: result exceeds the configured row limit")

// ErrReadOnly reports a statement rejected by the read-only transaction (§14.5).
var ErrReadOnly = errors.New("postgres: statement rejected by a read-only transaction")

// Error carries a classified failure reason alongside its cause.
//
// The reason is exposed as a method rather than a field so a consumer can read it
// through its own interface without importing this adapter (P7).
type Error struct {
	// Op names the failing operation: open, health, compile, or query.
	Op  string
	err error
	// Reason is read through Reason.
	reason telemetry.Reason
}

func newError(op string, reason telemetry.Reason, err error) *Error {
	return &Error{Op: op, err: err, reason: reason}
}

// Error renders the operation, reason, and cause.
func (e *Error) Error() string {
	return fmt.Sprintf("postgres %s: %s: %v", e.Op, e.reason, e.err)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.err }

// Reason reports the telemetry classification of this failure.
func (e *Error) Reason() telemetry.Reason { return e.reason }

// classify maps a driver or context error to a telemetry reason.
//
// Collection telemetry uses a closed label set, so an unmapped error must land on a
// defined reason rather than leak an error string (§13).
func classify(err error) telemetry.Reason {
	switch {
	case err == nil:
		return telemetry.ReasonUnknown
	// Cancellation and deadline both mean the collection did not finish in its
	// window; the scheduler owns the distinction between shutdown and timeout.
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return telemetry.ReasonTimeout
	case errors.Is(err, ErrResultTooLarge):
		return telemetry.ReasonResultTooLarge
	case errors.Is(err, ErrNotOpen):
		return telemetry.ReasonUnavailable
	case errors.Is(err, ErrReadOnly):
		return telemetry.ReasonQuery
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return classifySQLState(pgErr.Code)
	}

	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return telemetry.ReasonConnection
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return telemetry.ReasonConnection
	}

	// pgxpool and pgconn report a closed pool and a closed connection as bare
	// errors with no distinguishing type.
	if msg := err.Error(); strings.Contains(msg, "closed pool") || strings.Contains(msg, "conn closed") {
		return telemetry.ReasonConnection
	}
	return telemetry.ReasonQuery
}

// classifySQLState maps a PostgreSQL SQLSTATE to a telemetry reason.
func classifySQLState(code string) telemetry.Reason {
	switch code {
	case "57014": // query_canceled
		return telemetry.ReasonTimeout
	case "53300", // too_many_connections
		"53400", // configuration_limit_exceeded
		"57P01", // admin_shutdown
		"57P02", // crash_shutdown
		"57P03", // cannot_connect_now
		"58P01": // undefined_file
		return telemetry.ReasonConnection
	}
	switch class := sqlStateClass(code); class {
	case "08": // connection exception
		return telemetry.ReasonConnection
	case "22": // data exception, including numeric conversion in the server
		return telemetry.ReasonConversion
	default:
		return telemetry.ReasonQuery
	}
}

func sqlStateClass(code string) string {
	if len(code) < 2 {
		return ""
	}
	return code[:2]
}
