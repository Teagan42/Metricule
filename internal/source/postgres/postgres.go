// Package postgres implements the PostgreSQL source adapter (§9).
//
// Built on pgx directly rather than database/sql, because §9.2 requires a
// dedicated long-lived connection for LISTEN that a pool must never recycle.
//
// # Read-only guarantees
//
// Every collection runs in a repeatable-read, read-only transaction on a session
// whose default_transaction_read_only is forced on. Both are enforcement the
// server applies; neither depends on inspecting SQL text, which proves nothing
// (§14.5). Operators should still grant the collection role SELECT only (§14.2).
//
// # Result values
//
// Integers widen to int64, floats and numerics to float64, uuid to its canonical
// string. jsonb documents, arrays, and intervals pass through in pgx's decoded
// form, because inventing a scalar for them would be lossy.
//
// # Known limitations
//
// Querying and LISTEN/NOTIFY (§9.2) are implemented. Trigger management (§9.3) is
// M3, so Capabilities does not report it. pool.max_idle becomes a steady-state
// ceiling rather than a hard one, because pgx bounds idle connections from below.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/metricule/internal/source"
)

// ErrNotOpen reports use of a source before Open or after Close.
var ErrNotOpen = errors.New("postgres: source is not open")

// Capabilities this build delivers.
//
// Narrower than the declared table in internal/source/capabilities.go, which also
// claims TriggerManagement. That is M3 work; reporting it before provisioning
// exists would claim a capability this adapter cannot serve (rule 4).
var implemented = source.Capabilities{Query: true, Poll: true, Notifications: true}

// Register the factory. Capabilities are declared by internal/source (ADR 0005).
func init() { source.Register(source.TypePostgres, New) }

// Source queries PostgreSQL. Safe for concurrent use by multiple collectors.
type Source struct {
	id       string
	settings source.PostgresSettings
	// target is a credential-free description of where this source connects.
	target string
	log    *slog.Logger

	// cfg is resolved during construction so a malformed DSN fails at startup.
	cfg *pgxpool.Config

	mu   sync.RWMutex
	pool *pgxpool.Pool

	health health
}

// New builds an unopened PostgreSQL source from resolved settings.
func New(settings source.Settings) (source.Source, error) {
	s, ok := settings.Raw.(*source.PostgresSettings)
	if !ok || s == nil {
		return nil, fmt.Errorf("source %q: postgres adapter received %T settings", settings.ID, settings.Raw)
	}
	return NewSource(settings.ID, *s)
}

// NewSource builds an unopened source. Separate from New so callers holding
// concrete settings skip the any-typed registry path.
func NewSource(id string, settings source.PostgresSettings) (*Source, error) {
	cfg, err := poolConfig(settings)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", id, err)
	}
	return &Source{
		id:       id,
		settings: settings,
		target:   describeTarget(cfg),
		cfg:      cfg,
		log:      slog.With("source", id, "type", source.TypePostgres),
	}, nil
}

// ID returns the configured source name.
func (s *Source) ID() string { return s.id }

// Type returns the adapter type identifier.
func (s *Source) Type() string { return source.TypePostgres }

// Capabilities reports what this build supports, not what the type could support.
func (s *Source) Capabilities() source.Capabilities { return implemented }

// Close releases the pool. Safe to call repeatedly and before Open.
func (s *Source) Close() error {
	s.mu.Lock()
	pool := s.pool
	s.pool = nil
	s.mu.Unlock()

	if pool == nil {
		return nil
	}
	pool.Close()
	s.log.Debug("postgres source closed", "target", s.target)
	return nil
}

// Returns the live pool, or ErrNotOpen when the source is not open.
func (s *Source) acquirePool() (*pgxpool.Pool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pool == nil {
		return nil, fmt.Errorf("source %q: %w", s.id, ErrNotOpen)
	}
	return s.pool, nil
}

var _ source.Source = (*Source)(nil)

// Open creates the pool and verifies reachability.
func (s *Source) Open(ctx context.Context) error {
	return s.open(ctx)
}

// Health reports current reachability.
func (s *Source) Health(ctx context.Context) error {
	return s.checkHealth(ctx)
}
