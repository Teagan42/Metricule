// Package mysql implements the MySQL source adapter.
//
// # Read-only guarantees
//
// Every collection runs inside a read-only transaction (START TRANSACTION READ
// ONLY). Multi-statement DSNs are rejected, so one configured query cannot smuggle
// a second statement. Enforcement is the server transaction, not SQL string
// inspection (§14.5). Operators should still grant the collection user SELECT only.
//
// # Result values
//
// MySQL's text protocol returns most values as []byte. Values pass through as the
// driver decoded them: mapping parses numbers for samples and renders bytes as
// strings for labels, so a DECIMAL remains exact text until conversion.
//
// # Known limitations
//
// Querying and polling only. Change data capture, binlog observation, and
// notification triggers are not implemented.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// ErrNotOpen reports use of a source before Open or after Close.
var ErrNotOpen = errors.New("mysql: source is not open")

// Bounds connection acquisition at startup. Matches the PostgreSQL adapter so a
// datastore that is still starting is retried rather than fatal.
const (
	openAttempts   = 4
	openBackoff    = 250 * time.Millisecond
	openBackoffMax = 2 * time.Second
)

var implemented = source.Capabilities{Query: true, Poll: true}

func init() { source.Register(source.TypeMySQL, New) }

// Source queries MySQL. Safe for concurrent use by multiple collectors.
type Source struct {
	id       string
	settings source.MySQLSettings
	target   string
	log      *slog.Logger
	cfg      *driver.Config

	mu     sync.RWMutex
	db     *sql.DB
	opened bool
	closed bool
}

// New builds an unopened MySQL source from resolved settings.
func New(settings source.Settings) (source.Source, error) {
	s, ok := settings.Raw.(*source.MySQLSettings)
	if !ok || s == nil {
		return nil, fmt.Errorf("source %q: mysql adapter received %T settings", settings.ID, settings.Raw)
	}
	return NewSource(settings.ID, *s)
}

// NewSource builds an unopened source from concrete settings.
func NewSource(id string, settings source.MySQLSettings) (*Source, error) {
	cfg, err := mysqlConfig(settings)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", id, err)
	}
	return &Source{
		id:       id,
		settings: settings,
		target:   describeTarget(cfg),
		cfg:      cfg,
		log:      slog.With("source", id, "type", source.TypeMySQL),
	}, nil
}

// ID returns the configured source name.
func (s *Source) ID() string { return s.id }

// Type returns the adapter type identifier.
func (s *Source) Type() string { return source.TypeMySQL }

// Capabilities reports what this build supports.
func (s *Source) Capabilities() source.Capabilities { return implemented }

// Open creates the pool and verifies reachability with bounded retry.
func (s *Source) Open(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("source %q: mysql: source is closed", s.id)
	}
	s.opened = true
	if s.db != nil {
		s.mu.Unlock()
		return nil
	}
	db, err := sql.Open("mysql", s.cfg.FormatDSN())
	if err != nil {
		s.mu.Unlock()
		return newError("open", telemetry.ReasonConnection,
			fmt.Errorf("source %q: open %s: %w", s.id, s.target, err))
	}
	db.SetMaxOpenConns(s.settings.MaxOpen)
	db.SetMaxIdleConns(s.settings.MaxIdle)
	db.SetConnMaxIdleTime(time.Minute)
	s.db = db
	s.mu.Unlock()
	s.log.Info("mysql pool opened", "target", s.target,
		"max_open", s.settings.MaxOpen, "max_idle", s.settings.MaxIdle,
		"query_timeout", s.settings.QueryTimeout)

	var lastErr error
	delay := openBackoff
	for attempt := 1; attempt <= openAttempts; attempt++ {
		if lastErr = s.ping(ctx); lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		if attempt < openAttempts {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			delay = min(delay*2, openBackoffMax)
		}
	}
	return newError("open", classify(lastErr),
		fmt.Errorf("source %q: %s unreachable after %d attempts: %w", s.id, s.target, openAttempts, lastErr))
}

// Health pings the datastore.
func (s *Source) Health(ctx context.Context) error {
	db, err := s.handle()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.settings.QueryTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return newError("health", classify(err), fmt.Errorf("source %q: ping %s: %w", s.id, s.target, err))
	}
	return nil
}

// Close releases the pool. Idempotent.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	db := s.db
	s.db = nil
	if db == nil {
		return nil
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("source %q: close %s: %w", s.id, s.target, err)
	}
	return nil
}

func (s *Source) ping(ctx context.Context) error {
	db, err := s.handle()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.settings.QueryTimeout)
	defer cancel()
	return db.PingContext(ctx)
}

func (s *Source) handle() (*sql.DB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch {
	case s.closed:
		return nil, fmt.Errorf("source %q: %w", s.id, ErrNotOpen)
	case !s.opened || s.db == nil:
		return nil, fmt.Errorf("source %q: %w", s.id, ErrNotOpen)
	}
	return s.db, nil
}

// mysqlConfig validates resolved settings and builds a driver configuration.
//
// Errors never quote the DSN: it carries the password.
func mysqlConfig(settings source.MySQLSettings) (*driver.Config, error) {
	if strings.TrimSpace(settings.DSN) == "" {
		return nil, errors.New("dsn is empty")
	}
	if settings.MaxOpen <= 0 {
		return nil, fmt.Errorf("pool.max_open must be greater than zero, got %d", settings.MaxOpen)
	}
	if settings.MaxIdle < 0 {
		return nil, fmt.Errorf("pool.max_idle must not be negative, got %d", settings.MaxIdle)
	}
	if settings.MaxIdle > settings.MaxOpen {
		return nil, fmt.Errorf("pool.max_idle %d exceeds pool.max_open %d", settings.MaxIdle, settings.MaxOpen)
	}
	if settings.QueryTimeout <= 0 {
		return nil, fmt.Errorf("query_timeout must be greater than zero, got %s", settings.QueryTimeout)
	}

	cfg, err := driver.ParseDSN(settings.DSN)
	if err != nil {
		return nil, errors.New("dsn is not a valid MySQL connection string " +
			"(parser detail withheld because it echoes credentials)")
	}
	if cfg.MultiStatements {
		return nil, errors.New("dsn enables multiStatements, which would let one configured query run several statements; remove it")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return cfg, nil
}

// describeTarget renders a credential-free destination for diagnostics.
func describeTarget(cfg *driver.Config) string {
	addr := cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:3306"
	}
	if cfg.Net != "" && cfg.Net != "tcp" {
		addr = cfg.Net + ":" + addr
	}
	if cfg.DBName == "" {
		return addr
	}
	return addr + "/" + cfg.DBName
}
