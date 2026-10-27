package source

import "time"

// Adapter settings carry resolved configuration into a factory.
//
// Deliberately plain: these types hold no config or secrets types, so adapters
// depend on neither. internal/runtime translates configuration into them and
// resolves every secret reference first, which is why credentials appear here as
// plain strings and must never be logged.

// PostgresSettings configures the PostgreSQL adapter (§9).
type PostgresSettings struct {
	// DSN is resolved. Log only through secrets.RedactDSN.
	DSN          string
	MaxOpen      int
	MaxIdle      int
	QueryTimeout time.Duration

	TLSMode     string
	TLSRootCert string
	TLSCert     string
	TLSKey      string
}

// SQLiteSettings configures the SQLite adapter (§10). Read-only in v0.1 (AD-007).
type SQLiteSettings struct {
	Path                string
	BusyTimeout         time.Duration
	ChangeCheckInterval time.Duration
}

// HTTPSettings configures the HTTP/JSON adapter (§11).
type HTTPSettings struct {
	BaseURL string
	Timeout time.Duration
	Headers map[string]string

	// AuthType is "", "bearer", or "basic".
	AuthType string
	// Token is resolved bearer credential material. Never log.
	Token    string
	Username string
	// Password is resolved basic credential material. Never log.
	Password string

	// AllowedPaths restricts request paths. Empty permits any path under BaseURL.
	AllowedPaths     []string
	MaxResponseBytes int64
	MaxRedirects     int
	Retries          int
}
