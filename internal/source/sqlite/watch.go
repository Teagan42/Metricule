package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

var _ source.WatchableSource = (*Source)(nil)

const (
	// A failed observer reconnects with capped backoff rather than spinning on a
	// database that is briefly unreadable during a restore (§10.4).
	watchRetryBase = 250 * time.Millisecond
	watchRetryMax  = 30 * time.Second

	// A subscription that lasted this long is treated as established, so its
	// failure restarts backoff instead of accumulating toward the attempt cap.
	watchStableAfter = time.Minute

	// Used when settings carry no interval. Configuration always defaults this,
	// but a directly constructed source should not poll at 0s.
	defaultChangeCheckInterval = 2 * time.Second
)

// Watch observes database change with PRAGMA data_version until ctx is cancelled.
//
// data_version is connection-local: it advances when a different connection
// commits. The observer therefore pins one connection and compares successive
// reads on it. It cannot name the changed table, so every event carries
// ScopeUnknown and invalidates every plan on this source.
func (s *Source) Watch(ctx context.Context, spec source.WatchSpec, events chan<- source.ChangeEvent) error {
	if spec.Channel != "" {
		return fmt.Errorf("source %q: sqlite has no notification channels; channel %q cannot be observed",
			s.id, spec.Channel)
	}

	interval := s.changeInterval
	if interval <= 0 {
		interval = defaultChangeCheckInterval
	}

	delay := watchRetryBase
	for {
		started := time.Now()
		err := s.observe(ctx, interval, events)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		// A replaced file is a normal reconnect, not a failure: re-establish at once.
		if err == nil || time.Since(started) >= watchStableAfter {
			delay = watchRetryBase
			if err == nil {
				continue
			}
		}

		// File replacement, restore, and transient read failures are all expected
		// for a database this process does not own. Retry until cancelled.
		s.log.Warn("sqlite change observation failed", "path", s.path, "error", err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, watchRetryMax)
	}
}

// observe runs one subscription: connect, report readiness, then watch for change.
//
// Returns when the file is replaced or a read fails; the caller re-establishes.
func (s *Source) observe(ctx context.Context, interval time.Duration, events chan<- source.ChangeEvent) error {
	if err := s.watchReady(); err != nil {
		return err
	}

	fi, err := os.Stat(s.path)
	if err != nil {
		return s.pathError("stat", err)
	}

	// A separate pool keeps the pinned observer from consuming a collection
	// connection (P5). It is read-only for the same reason as every other
	// connection (AD-007).
	db, err := sql.Open(driverName, s.dsn)
	if err != nil {
		return fmt.Errorf("source %q: open observer for %s: %w", s.id, s.path, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("source %q: pin observer for %s: %w", s.id, s.path, err)
	}
	defer func() { _ = conn.Close() }()

	last, err := dataVersion(ctx, conn)
	if err != nil {
		return fmt.Errorf("source %q: observe %s: %w", s.id, s.path, err)
	}
	emit(events, source.ChangeEvent{SourceID: s.id, Kind: source.KindReconnect})

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// A replaced inode keeps its old contents on this pinned connection, so
			// data_version would stop advancing without this check.
			current, err := os.Stat(s.path)
			if err != nil {
				return s.pathError("stat", err)
			}
			if !os.SameFile(fi, current) {
				return nil
			}

			version, err := dataVersion(ctx, conn)
			if err != nil {
				return fmt.Errorf("source %q: observe %s: %w", s.id, s.path, err)
			}
			if version == last {
				continue
			}
			last = version
			emit(events, source.ChangeEvent{SourceID: s.id, Kind: source.KindToken})
		}
	}
}

// Checks that the source was opened and has not been closed.
func (s *Source) watchReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return fmt.Errorf("source %q: %w", s.id, errClosed)
	case !s.opened:
		return fmt.Errorf("source %q: %w", s.id, errOpenRequired)
	}
	return nil
}

// Reads the connection-local change counter.
func dataVersion(ctx context.Context, conn *sql.Conn) (int64, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	var version int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read data_version: %w", err)
	}
	return version, nil
}

// Delivers without blocking. The scheduler coalesces, so dropping when the buffer
// is full loses nothing: one pending invalidation already means "refresh" (rule 10).
func emit(events chan<- source.ChangeEvent, ev source.ChangeEvent) {
	ev.At = time.Now()
	select {
	case events <- ev:
	default:
	}
}
