package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/teagan42/metricule/internal/source"
)

var _ source.WatchableSource = (*Source)(nil)

const (
	// A listener that cannot establish or keep its subscription retries with
	// capped backoff. PostgreSQL discards notifications while no session is
	// listening, and the plan's fallback interval repairs the gap (AD-005).
	listenRetryBase = 250 * time.Millisecond
	listenRetryMax  = 30 * time.Second

	// A subscription that lasted this long is treated as established, so its
	// failure restarts backoff instead of accumulating toward the attempt cap.
	listenStableAfter = time.Minute
)

// Watch listens for NOTIFY on spec.Channel until ctx is cancelled.
//
// The session is dedicated: a pooled connection must never carry LISTEN, because
// returning it to the pool would drop the subscription (§9.2). Each
// re-establishment emits KindReconnect so the scheduler reconciles the window it
// could not observe.
func (s *Source) Watch(ctx context.Context, spec source.WatchSpec, events chan<- source.ChangeEvent) error {
	if strings.TrimSpace(spec.Channel) == "" {
		return fmt.Errorf("source %q: postgres change observation requires a channel", s.id)
	}
	if _, err := s.acquirePool(); err != nil {
		return err
	}

	// Quoted so a channel containing punctuation is a single identifier rather
	// than injected SQL. Configuration does not restrict channel spelling.
	statement := listenStatement(spec.Channel)
	scopes := make(map[string]struct{}, len(spec.Tables))
	for _, t := range spec.Tables {
		scopes[t] = struct{}{}
	}

	delay := listenRetryBase
	established := false
	for {
		started := time.Now()
		err := s.listen(ctx, statement, scopes, events, &established)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if err == nil || time.Since(started) >= listenStableAfter {
			delay = listenRetryBase
			if err == nil {
				continue
			}
		}

		// Delivery during a disconnect is not durable; retry until cancelled and
		// let the plan's fallback interval repair the gap (AD-005).
		s.log.Warn("postgres listener failed",
			"target", s.target, "channel", spec.Channel, "error", err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, listenRetryMax)
	}
}

// listen runs one subscription: connect, LISTEN, report readiness, then wait.
func (s *Source) listen(ctx context.Context, statement string, scopes map[string]struct{}, events chan<- source.ChangeEvent, established *bool) error {
	cfg := s.cfg.ConnConfig.Copy()
	// The pool's handler turns cancellation into a server-side cancel request and
	// waits up to ten seconds for a reply. A listener is blocked reading, not
	// running a statement, so no reply comes and shutdown stalls. Expire the read
	// immediately instead.
	cfg.BuildContextWatcherHandler = func(pgConn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.DeadlineContextWatcherHandler{Conn: pgConn.Conn()}
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect listener for %s: %w", s.target, err)
	}
	// pgx closes the socket even when the context is already cancelled.
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, statement); err != nil {
		return fmt.Errorf("%s: %w", statement, err)
	}

	// The initial subscription is not a reconnect; every later one is.
	if *established {
		s.health.recordReconnect()
	}
	*established = true

	// Readiness, not an observed change: the scheduler reconciles everything the
	// baseline collection may have missed before now (§9.2).
	emit(events, source.ChangeEvent{SourceID: s.id, Kind: source.KindReconnect})

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("wait for notification: %w", err)
		}
		emit(events, source.ChangeEvent{
			SourceID: s.id,
			Kind:     source.KindNotify,
			Scope:    scopeFor(n.Payload, scopes),
		})
	}
}

// Maps a payload to an invalidation scope without trusting arbitrary content.
//
// Only a declared table narrows the scope. Anything else is unknown, which
// invalidates every plan on the source: filtering on a typo or an application
// message would silently drop the refresh.
func scopeFor(payload string, scopes map[string]struct{}) string {
	if _, ok := scopes[payload]; ok {
		return payload
	}
	return source.ScopeUnknown
}

// Builds the LISTEN statement for a configured channel.
func listenStatement(channel string) string {
	return "LISTEN " + pgx.Identifier{channel}.Sanitize()
}

// Delivers without blocking. The scheduler coalesces, so dropping when the buffer
// is full loses nothing (rule 10).
func emit(events chan<- source.ChangeEvent, ev source.ChangeEvent) {
	ev.At = time.Now()
	select {
	case events <- ev:
	default:
	}
}
