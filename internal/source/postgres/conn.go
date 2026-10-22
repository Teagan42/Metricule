package postgres

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/metricule/internal/source"
)

const (
	// Keeps a pool that is down from refilling as fast as the health checker runs.
	defaultConnectTimeout = 10 * time.Second
	// Approximates pool.max_idle: pgx caps a minimum idle count, not a maximum, so
	// connections beyond max_idle are released by idle reaping instead.
	defaultConnIdleTime = time.Minute
	applicationName     = "metricule"
)

// poolConfig translates resolved settings into a pgx pool configuration.
//
// Errors never quote the DSN: pgx parse failures echo their input, which carries
// the password.
func poolConfig(s source.PostgresSettings) (*pgxpool.Config, error) {
	if strings.TrimSpace(s.DSN) == "" {
		return nil, errors.New("dsn is empty")
	}
	if s.MaxOpen <= 0 {
		return nil, fmt.Errorf("pool.max_open must be greater than zero, got %d", s.MaxOpen)
	}
	if s.MaxIdle < 0 {
		return nil, fmt.Errorf("pool.max_idle must not be negative, got %d", s.MaxIdle)
	}
	if s.MaxIdle > s.MaxOpen {
		return nil, fmt.Errorf("pool.max_idle %d exceeds pool.max_open %d", s.MaxIdle, s.MaxOpen)
	}
	if s.MaxOpen > math.MaxInt32 {
		return nil, fmt.Errorf("pool.max_open %d is not a usable connection count", s.MaxOpen)
	}
	if s.QueryTimeout <= 0 {
		return nil, fmt.Errorf("query_timeout must be greater than zero, got %s", s.QueryTimeout)
	}

	cfg, err := pgxpool.ParseConfig(withTLSParams(s.DSN, s))
	if err != nil {
		return nil, errors.New("dsn is not a valid PostgreSQL connection string " +
			"(parser detail withheld because it echoes credentials)")
	}

	cfg.MaxConns = connCount(s.MaxOpen)
	cfg.MinIdleConns = connCount(s.MaxIdle)
	cfg.MaxConnIdleTime = defaultConnIdleTime

	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = defaultConnectTimeout
	}

	applyRuntimeParams(cfg.ConnConfig.RuntimeParams, s.QueryTimeout)

	// Cancel server-side instead of dropping the socket, so a timed-out collection
	// neither leaves the query running nor discards a pooled connection.
	cfg.ConnConfig.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn:               c,
			CancelRequestDelay: 0,
			DeadlineDelay:      defaultConnectTimeout,
		}
	}

	return cfg, nil
}

// connCount narrows a validated connection bound to pgx's int32 fields.
func connCount(v int) int32 {
	switch {
	case v < 0:
		return 0
	case v > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(v)
	}
}

// applyRuntimeParams enforces read-only sessions and server-side query bounds.
//
// default_transaction_read_only overrides any DSN value: §14.5 requires enforcement
// that does not depend on inspecting SQL text.
func applyRuntimeParams(params map[string]string, queryTimeout time.Duration) {
	params["default_transaction_read_only"] = "on"

	ms := strconv.FormatInt(queryTimeout.Milliseconds(), 10)
	setIfAbsent(params, "statement_timeout", ms)
	setIfAbsent(params, "idle_in_transaction_session_timeout", ms)
	setIfAbsent(params, "application_name", applicationName)
}

func setIfAbsent(params map[string]string, key, value string) {
	if _, ok := params[key]; !ok {
		params[key] = value
	}
}

// withTLSParams folds TLS settings into the connection string so pgx interprets
// libpq sslmode semantics. Reimplementing those semantics here would be a
// proprietary TLS policy (§4.2).
//
// Returns the DSN unchanged when nothing is configured, or when a URL-form DSN does
// not parse; pgxpool.ParseConfig then reports the syntax error.
func withTLSParams(dsn string, s source.PostgresSettings) string {
	params := [][2]string{
		{"sslmode", s.TLSMode},
		{"sslrootcert", s.TLSRootCert},
		{"sslcert", s.TLSCert},
		{"sslkey", s.TLSKey},
	}
	set := make([][2]string, 0, len(params))
	for _, p := range params {
		if p[1] != "" {
			set = append(set, p)
		}
	}
	if len(set) == 0 {
		return dsn
	}

	if isURLDSN(dsn) {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn
		}
		q := u.Query()
		for _, p := range set {
			q.Set(p[0], p[1])
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	var b strings.Builder
	b.WriteString(dsn)
	for _, p := range set {
		b.WriteString(" " + p[0] + "=" + quoteKeywordValue(p[1]))
	}
	return b.String()
}

func isURLDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// quoteKeywordValue escapes a libpq keyword/value setting.
func quoteKeywordValue(v string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v)
	return "'" + escaped + "'"
}

// describeTarget renders a credential-free connection target for logs (§14.3).
func describeTarget(cfg *pgxpool.Config) string {
	c := cfg.ConnConfig
	host := c.Host
	if host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s:%d/%s", host, c.Port, c.Database)
}
