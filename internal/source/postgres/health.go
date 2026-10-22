package postgres

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/metricule/internal/telemetry"
)

// Backoff bounds for reconnect attempts. A down datastore must not be probed as
// fast as the scheduler asks, and recovery must not be delayed beyond the cap.
const (
	backoffBase = 250 * time.Millisecond
	backoffMax  = 10 * time.Second
	// Attempts Open makes before reporting the source unreachable. A datastore that
	// is still starting is the common case for a sidecar.
	openAttempts = 4
)

// Diagnostics is an explicit health report for operator-facing diagnostics (§9.1).
type Diagnostics struct {
	// Target is credential-free: host:port/database.
	Target string
	Up     bool
	// CheckedAt is zero until the first health check.
	CheckedAt time.Time
	// LastSuccess is zero until the first successful check.
	LastSuccess time.Time
	// ConsecutiveFailures resets on any successful check.
	ConsecutiveFailures int
	// Reconnects counts down-to-up transitions. Monotonic for the life of the
	// Source, including across Close and a later Open: internal/runtime derives a
	// counter delta from it, so resetting it would retract published samples.
	Reconnects int
	// LastError is the most recent failure, retained after recovery.
	LastError error
	// Reason classifies LastError.
	Reason telemetry.Reason
}

// Tracks reachability across checks so backoff and reconnects are observable.
type health struct {
	mu          sync.Mutex
	up          bool
	checkedAt   time.Time
	lastSuccess time.Time
	failures    int
	reconnects  int
	retryAfter  time.Time
	lastErr     error
	reason      telemetry.Reason
}

// Reports a success and whether it closed an outage.
func (h *health) succeed(now time.Time) (reconnected bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	reconnected = h.failures > 0
	if reconnected {
		h.reconnects++
	}
	h.up = true
	h.checkedAt = now
	h.lastSuccess = now
	h.failures = 0
	h.retryAfter = time.Time{}
	return reconnected
}

func (h *health) fail(now time.Time, err error, reason telemetry.Reason) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.up = false
	h.checkedAt = now
	h.failures++
	h.lastErr = err
	h.reason = reason
	h.retryAfter = now.Add(backoffFor(h.failures))
}

// Reports the cached failure while the current backoff window is open.
func (h *health) gated(now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastErr == nil || !now.Before(h.retryAfter) {
		return nil
	}
	return h.lastErr
}

func (h *health) snapshot() Diagnostics {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Diagnostics{
		Up:                  h.up,
		CheckedAt:           h.checkedAt,
		LastSuccess:         h.lastSuccess,
		ConsecutiveFailures: h.failures,
		Reconnects:          h.reconnects,
		LastError:           h.lastErr,
		Reason:              h.reason,
	}
}

// backoffFor returns the delay before retry number n, capped at backoffMax.
//
// Deterministic: the caller is a periodic health check, so there is no thundering
// herd to spread and a predictable recovery bound is worth more than jitter.
func backoffFor(attempt int) time.Duration {
	if attempt <= 1 {
		return backoffBase
	}
	d := backoffBase
	for range attempt - 1 {
		d *= 2
		if d >= backoffMax {
			return backoffMax
		}
	}
	return d
}

// Diagnostics reports the latest health state without contacting the database.
func (s *Source) Diagnostics() Diagnostics {
	d := s.health.snapshot()
	d.Target = s.target
	return d
}

// Creates the pool and verifies reachability with bounded retry.
//
// The pool survives a failed verification: pgx reconnects on its own, so a
// datastore that is down at startup must not require a Metricule restart once it
// returns.
func (s *Source) open(ctx context.Context) error {
	s.mu.Lock()
	if s.pool != nil {
		s.mu.Unlock()
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, s.cfg)
	if err != nil {
		s.mu.Unlock()
		return newError("open", telemetry.ReasonConnection,
			fmt.Errorf("source %q: build pool for %s: %w", s.id, s.target, err))
	}
	s.pool = pool
	s.mu.Unlock()

	s.log.Info("postgres pool opened", "target", s.target,
		"max_open", s.settings.MaxOpen, "max_idle", s.settings.MaxIdle,
		"query_timeout", s.settings.QueryTimeout)

	var lastErr error
	for attempt := 1; attempt <= openAttempts; attempt++ {
		if lastErr = s.ping(ctx, pool); lastErr == nil {
			s.health.succeed(time.Now())
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		if attempt < openAttempts {
			if err := sleep(ctx, backoffFor(attempt)); err != nil {
				break
			}
		}
	}

	reason := classify(lastErr)
	s.health.fail(time.Now(), lastErr, reason)
	return newError("open", reason,
		fmt.Errorf("source %q: %s unreachable after %d attempts: %w",
			s.id, s.target, openAttempts, lastErr))
}

// Pings the datastore, suppressing probes inside the current backoff window.
func (s *Source) checkHealth(ctx context.Context) error {
	pool, err := s.acquirePool()
	if err != nil {
		return newError("health", telemetry.ReasonUnavailable, err)
	}

	now := time.Now()
	if cached := s.health.gated(now); cached != nil {
		return newError("health", s.health.snapshot().Reason,
			fmt.Errorf("source %q: %s unreachable, retry deferred by backoff: %w", s.id, s.target, cached))
	}

	if err := s.ping(ctx, pool); err != nil {
		reason := classify(err)
		s.health.fail(time.Now(), err, reason)
		d := s.health.snapshot()
		s.log.Warn("postgres health check failed", "target", s.target,
			"reason", string(reason), "consecutive_failures", d.ConsecutiveFailures, "error", err)
		return newError("health", reason,
			fmt.Errorf("source %q: %s unreachable: %w", s.id, s.target, err))
	}

	if s.health.succeed(time.Now()) {
		// Notification delivery is not durable across a disconnect, so a reconnect
		// forces the scheduler to reconcile (§9.2).
		s.log.Info("postgres source reconnected", "target", s.target,
			"reconnects", s.health.snapshot().Reconnects)
	}
	return nil
}

// Verifies one connection within the configured query timeout.
func (s *Source) ping(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, s.settings.QueryTimeout)
	defer cancel()
	return pool.Ping(ctx)
}

// Waits for d, returning ctx.Err() if the context ends first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
