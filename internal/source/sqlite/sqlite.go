// Package sqlite implements the read-only SQLite source adapter (§10).
//
// AD-007 is absolute: collection never writes the database. Connections are
// opened with mode=ro, the journal mode is never altered, and no trigger is
// installed. The file belongs to the monitored application.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/teagan42/metricule/internal/source"

	// Pure-Go driver, registered as "sqlite". ADR 0004 disables cgo project-wide,
	// so a binding such as mattn/go-sqlite3 is not an option.
	_ "modernc.org/sqlite"
)

const (
	driverName = "sqlite"

	// Aggregate reads are short, so a small pool bounds concurrent collections
	// without starving them (P5).
	maxOpenConns = 4
	maxIdleConns = 2

	// Reaps idle connections so a cached handle does not pin a replaced or
	// deleted inode. Deliberately not SetConnMaxLifetime: §10.2 needs one
	// long-lived observer connection in M2, and a lifetime cap would recycle it,
	// which silently invalidates data_version comparison.
	idleTimeout = time.Minute

	// Backstop for a caller that supplies no deadline. SQLiteSettings carries no
	// query_timeout, and an unbounded scan violates P5.
	defaultQueryTimeout = 30 * time.Second
)

var (
	// ErrTooManyRows reports a result larger than the configured MaxRows.
	// §8.7 forbids silent truncation, so the collection fails instead.
	ErrTooManyRows = errors.New("result exceeds max rows")

	errOpenRequired = errors.New("source is not open")
	errClosed       = errors.New("source is closed")
)

func init() {
	source.Register(source.TypeSQLite, New)
}

// Source is a read-only SQLite adapter instance. Safe for concurrent use.
type Source struct {
	id                  string
	path                string
	dsn                 string
	changeCheckInterval time.Duration

	// Guards the connection pool and the identity of the file it was opened on.
	// Queries run outside it: database/sql is already concurrency safe, and a
	// racing reopen only closes connections once they are returned.
	mu     sync.Mutex
	db     *sql.DB
	ident  os.FileInfo
	opened bool
	closed bool
}

// New constructs an unopened SQLite source from resolved settings.
func New(settings source.Settings) (source.Source, error) {
	cfg, ok := settings.Raw.(*source.SQLiteSettings)
	if !ok {
		return nil, fmt.Errorf("source %q: expected *source.SQLiteSettings, got %T", settings.ID, settings.Raw)
	}
	if cfg.Path == "" {
		return nil, fmt.Errorf("source %q: path is required", settings.ID)
	}
	dsn, err := readOnlyDSN(cfg.Path, cfg.BusyTimeout)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", settings.ID, err)
	}
	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("source %q: resolve path %q: %w", settings.ID, cfg.Path, err)
	}
	return &Source{
		id:                  settings.ID,
		path:                abs,
		dsn:                 dsn,
		changeCheckInterval: cfg.ChangeCheckInterval,
	}, nil
}

// ID returns the configured source name.
func (s *Source) ID() string { return s.id }

// Type returns the adapter type identifier.
func (s *Source) Type() string { return source.TypeSQLite }

// Capabilities reports the capabilities Agent 0 declared for this type.
//
// Read from the registry rather than restated, so an adapter cannot drift from
// the configuration contract (rule 3).
func (s *Source) Capabilities() source.Capabilities {
	c, _ := source.CapabilitiesFor(source.TypeSQLite)
	return c
}

// Open connects and verifies that the connection is read-only and reachable.
func (s *Source) Open(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("source %q: %w", s.id, errClosed)
	}
	// Recorded before the attempt succeeds: a file that is absent at startup must
	// still recover once it appears, rather than staying permanently unopened
	// (§10.4).
	s.opened = true
	if s.db != nil {
		return nil
	}
	return s.connectLocked(ctx)
}

// Query executes a compiled statement. Errors rather than truncating at MaxRows.
func (s *Source) Query(ctx context.Context, req source.QueryRequest) (source.ResultSet, error) {
	sp, ok := req.Spec.(*spec)
	if !ok {
		return source.ResultSet{}, fmt.Errorf("source %q: query spec %T was not compiled by the sqlite adapter", s.id, req.Spec)
	}

	db, err := s.handle(ctx)
	if err != nil {
		return source.ResultSet{}, err
	}

	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	rows, err := db.QueryContext(ctx, sp.sql)
	if err != nil {
		return source.ResultSet{}, fmt.Errorf("source %q: metric %q: execute: %w", s.id, sp.metric, err)
	}
	defer func() { _ = rows.Close() }()

	rs, err := scanRows(rows, req.MaxRows)
	if err != nil {
		return source.ResultSet{}, fmt.Errorf("source %q: metric %q: read result: %w", s.id, sp.metric, err)
	}
	return rs, nil
}

// Health reports whether the database file is present and readable now.
func (s *Source) Health(ctx context.Context) error {
	db, err := s.handle(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("source %q: ping %s: %w", s.id, s.path, err)
	}
	return nil
}

// Close releases the connection pool. Idempotent.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if err := s.discardLocked(); err != nil {
		return fmt.Errorf("source %q: close %s: %w", s.id, s.path, err)
	}
	return nil
}

// Returns a pool known to point at the current file, reopening when the
// application has replaced or restored it (§10.4).
func (s *Source) handle(ctx context.Context) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, fmt.Errorf("source %q: %w", s.id, errClosed)
	case !s.opened:
		return nil, fmt.Errorf("source %q: %w", s.id, errOpenRequired)
	}

	if s.db != nil {
		fi, err := os.Stat(s.path)
		if err != nil {
			// Drop the pool now so its connections do not pin a deleted inode.
			_ = s.discardLocked()
			return nil, s.pathError("stat", err)
		}
		if os.SameFile(fi, s.ident) {
			return s.db, nil
		}
		_ = s.discardLocked()
	}

	if err := s.connectLocked(ctx); err != nil {
		return nil, err
	}
	return s.db, nil
}

func (s *Source) connectLocked(ctx context.Context) error {
	fi, err := os.Stat(s.path)
	if err != nil {
		return s.pathError("stat", err)
	}

	// The driver reports an unreadable file as a bare SQLITE_CANTOPEN, which tells
	// an operator nothing. Probing first keeps the actionable message.
	probe, err := os.Open(s.path)
	if err != nil {
		return s.pathError("open", err)
	}
	if err = probe.Close(); err != nil {
		return s.pathError("close", err)
	}

	db, err := sql.Open(driverName, s.dsn)
	if err != nil {
		return fmt.Errorf("source %q: open %s: %w", s.id, s.path, err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(idleTimeout)

	if err := verifyReadOnly(ctx, db); err != nil {
		_ = db.Close()
		// The file itself is readable, so the remaining cause is almost always the
		// WAL sidecars: reading a WAL database needs the -wal file plus a -shm
		// index that SQLite creates beside it.
		return fmt.Errorf("source %q: open %s: %w; a WAL database also requires a readable %s-wal and a directory that permits creating %s-shm",
			s.id, s.path, err, s.path, s.path)
	}

	s.db, s.ident = db, fi
	return nil
}

func (s *Source) discardLocked() error {
	db := s.db
	s.db, s.ident = nil, nil
	if db == nil {
		return nil
	}
	return db.Close()
}

// Produces an actionable error for the two filesystem failures a sidecar hits.
func (s *Source) pathError(op string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("source %q: %s %s: database file is absent; check the volume mount: %w", s.id, op, s.path, err)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("source %q: %s %s: permission denied; the file and every parent directory must be readable by the exporter user, uid 65532 in the published image: %w", s.id, op, s.path, err)
	default:
		return fmt.Errorf("source %q: %s %s: %w", s.id, op, s.path, err)
	}
}

// Confirms reachability and that AD-007 is in force on a fresh pool.
func verifyReadOnly(ctx context.Context, db *sql.DB) error {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return fmt.Errorf("read query_only: %w", err)
	}
	if queryOnly != 1 {
		return errors.New("connection is writable; refusing to collect from a read-write SQLite connection (AD-007)")
	}
	return nil
}

// Bounds a query when the caller supplies no deadline. A caller deadline always
// wins, so this never shortens a configured collection timeout.
func withQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultQueryTimeout)
}
