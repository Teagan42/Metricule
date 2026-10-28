package mysql

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"net"

	driver "github.com/go-sql-driver/mysql"

	"github.com/teagan42/metricule/internal/telemetry"
)

// ErrResultTooLarge reports a result larger than the configured row limit.
var ErrResultTooLarge = errors.New("mysql: result exceeds the configured row limit")

// ErrReadOnly reports a statement rejected by the read-only transaction.
var ErrReadOnly = errors.New("mysql: statement rejected by a read-only transaction")

// MySQL server error codes used for classification. A closed label set is required
// by collection telemetry, so unknown codes degrade to a generic query failure.
const (
	erCantExecuteInReadOnlyTransaction = 1792
	erAccessDenied                     = 1044
	erAccessDeniedNoDb                 = 1045
	erTableAccessDenied                = 1142
	erColumnAccessDenied               = 1143
	erNoSuchTable                      = 1146
	erLockWaitTimeout                  = 1205
)

// Error carries a classified failure reason alongside its cause.
type Error struct {
	Op     string
	err    error
	reason telemetry.Reason
}

func newError(op string, reason telemetry.Reason, err error) *Error {
	return &Error{Op: op, reason: reason, err: err}
}

// Error renders the operation, reason, and cause.
func (e *Error) Error() string {
	return fmt.Sprintf("mysql %s: %s: %v", e.Op, e.reason, e.err)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.err }

// Reason reports the telemetry classification of this failure.
func (e *Error) Reason() telemetry.Reason { return e.reason }

// classify maps a driver or context error to a closed telemetry reason.
func classify(err error) telemetry.Reason {
	switch {
	case err == nil:
		return telemetry.ReasonUnknown
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return telemetry.ReasonTimeout
	case errors.Is(err, ErrResultTooLarge):
		return telemetry.ReasonResultTooLarge
	case errors.Is(err, ErrNotOpen):
		return telemetry.ReasonUnavailable
	case errors.Is(err, ErrReadOnly):
		return telemetry.ReasonQuery
	}

	var myErr *driver.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case erCantExecuteInReadOnlyTransaction:
			return telemetry.ReasonQuery
		case erLockWaitTimeout:
			return telemetry.ReasonTimeout
		case erAccessDenied, erAccessDeniedNoDb, erTableAccessDenied, erColumnAccessDenied:
			return telemetry.ReasonQuery
		case erNoSuchTable:
			return telemetry.ReasonQuery
		}
		if myErr.Number >= 2000 {
			// Client-side connection errors (server gone, lost connection, can't
			// connect).
			return telemetry.ReasonConnection
		}
		return telemetry.ReasonQuery
	}

	if errors.Is(err, sqldriver.ErrBadConn) {
		return telemetry.ReasonConnection
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return telemetry.ReasonConnection
	}
	return telemetry.ReasonQuery
}

// Reports whether an error came from the read-only transaction guard.
func isReadOnly(err error) bool {
	var myErr *driver.MySQLError
	return errors.As(err, &myErr) && myErr.Number == erCantExecuteInReadOnlyTransaction
}
