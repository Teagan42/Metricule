package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/teagan42/metricule/internal/telemetry"
)

func TestClassify(t *testing.T) {
	cases := map[string]struct {
		err  error
		want telemetry.Reason
	}{
		"nil":                 {nil, telemetry.ReasonUnknown},
		"deadline":            {context.DeadlineExceeded, telemetry.ReasonTimeout},
		"cancelled":           {context.Canceled, telemetry.ReasonTimeout},
		"wrapped deadline":    {fmt.Errorf("query: %w", context.DeadlineExceeded), telemetry.ReasonTimeout},
		"too large":           {fmt.Errorf("rows: %w", ErrResultTooLarge), telemetry.ReasonResultTooLarge},
		"not open":            {fmt.Errorf("source: %w", ErrNotOpen), telemetry.ReasonUnavailable},
		"read only":           {fmt.Errorf("tx: %w", ErrReadOnly), telemetry.ReasonQuery},
		"query cancelled":     {&pgconn.PgError{Code: "57014"}, telemetry.ReasonTimeout},
		"admin shutdown":      {&pgconn.PgError{Code: "57P01"}, telemetry.ReasonConnection},
		"cannot connect now":  {&pgconn.PgError{Code: "57P03"}, telemetry.ReasonConnection},
		"too many conns":      {&pgconn.PgError{Code: "53300"}, telemetry.ReasonConnection},
		"connection failure":  {&pgconn.PgError{Code: "08006"}, telemetry.ReasonConnection},
		"syntax error":        {&pgconn.PgError{Code: "42601"}, telemetry.ReasonQuery},
		"no privilege":        {&pgconn.PgError{Code: "42501"}, telemetry.ReasonQuery},
		"read only tx":        {&pgconn.PgError{Code: "25006"}, telemetry.ReasonQuery},
		"division by zero":    {&pgconn.PgError{Code: "22012"}, telemetry.ReasonConversion},
		"short sqlstate":      {&pgconn.PgError{Code: "4"}, telemetry.ReasonQuery},
		"network":             {&net.OpError{Op: "dial", Err: errors.New("refused")}, telemetry.ReasonConnection},
		"closed pool":         {errors.New("closed pool"), telemetry.ReasonConnection},
		"closed conn":         {errors.New("conn closed"), telemetry.ReasonConnection},
		"unclassified":        {errors.New("boom"), telemetry.ReasonQuery},
		"timeout net wrapped": {fmt.Errorf("read: %w", &net.DNSError{IsTimeout: true}), telemetry.ReasonConnection},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorWrapsCause(t *testing.T) {
	cause := fmt.Errorf("outer: %w", ErrResultTooLarge)
	err := newError("query", telemetry.ReasonResultTooLarge, cause)

	if !errors.Is(err, ErrResultTooLarge) {
		t.Error("errors.Is did not reach the sentinel")
	}
	if err.Reason() != telemetry.ReasonResultTooLarge {
		t.Errorf("Reason() = %q", err.Reason())
	}
	// A consumer must read the reason without importing this package.
	var reporter interface{ Reason() telemetry.Reason }
	if !errors.As(error(err), &reporter) {
		t.Fatal("error does not satisfy the reason reporter interface")
	}
}

func TestBackoffIsBounded(t *testing.T) {
	want := []time.Duration{
		backoffBase,
		2 * backoffBase,
		4 * backoffBase,
		8 * backoffBase,
		16 * backoffBase,
		32 * backoffBase,
		backoffMax,
		backoffMax,
	}
	for i, w := range want {
		if got := backoffFor(i + 1); got != w {
			t.Errorf("backoffFor(%d) = %s, want %s", i+1, got, w)
		}
	}
	if got := backoffFor(0); got != backoffBase {
		t.Errorf("backoffFor(0) = %s, want %s", got, backoffBase)
	}
	if got := backoffFor(1000); got != backoffMax {
		t.Errorf("backoffFor(1000) = %s, want the cap %s", got, backoffMax)
	}
}

func TestHealthStateTracksOutages(t *testing.T) {
	var h health
	now := time.Now()

	if reconnected := h.succeed(now); reconnected {
		t.Error("first success must not count as a reconnect")
	}
	h.fail(now, errors.New("down"), telemetry.ReasonConnection)
	h.fail(now, errors.New("down"), telemetry.ReasonConnection)

	d := h.snapshot()
	if d.Up || d.ConsecutiveFailures != 2 {
		t.Errorf("snapshot = %+v, want down with 2 failures", d)
	}
	if h.gated(now) == nil {
		t.Error("a probe inside the backoff window must be suppressed")
	}
	if h.gated(now.Add(backoffMax)) != nil {
		t.Error("a probe after the backoff window must be allowed")
	}

	if reconnected := h.succeed(now.Add(time.Second)); !reconnected {
		t.Error("recovery after failures must report a reconnect")
	}
	d = h.snapshot()
	if !d.Up || d.ConsecutiveFailures != 0 || d.Reconnects != 1 {
		t.Errorf("snapshot = %+v, want up with 1 reconnect", d)
	}
	if h.gated(now.Add(time.Second)) != nil {
		t.Error("a healthy source must not be gated")
	}
}

func TestDiagnosticsReportTarget(t *testing.T) {
	s := testSource(t)
	d := s.Diagnostics()
	if d.Target != "db.invalid:5432/app" {
		t.Errorf("Target = %q, want db.invalid:5432/app", d.Target)
	}
	if d.Up {
		t.Error("an unopened source must not report up")
	}
}
