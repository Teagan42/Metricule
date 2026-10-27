// Package runtime compiles validated configuration into executable runtime objects.
//
// It is the only place that resolves secret references, so adapters receive plain
// values and never depend on internal/secrets (§14.3).
package runtime

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

// Warns when configuration asks for a capability this build does not implement.
//
// Accepting the block silently would make an operator believe notification triggers
// are installed when nothing was provisioned (rule 4).
func warnIgnoredTriggerManagement(log *slog.Logger, cfg *config.Config) {
	for _, name := range sortedNames(cfg.Sources) {
		p := cfg.Sources[name].Postgres
		if p == nil || p.Triggers == nil || !p.Triggers.Manage {
			continue
		}
		log.Warn("triggers.manage is ignored until M3",
			"source", name, "schema", p.Triggers.Schema)
	}
}

// BuildSources constructs every configured source.
//
// Secrets resolve here, which is the first point at which a missing environment
// variable or unreadable secret file becomes an error (§5.4).
func BuildSources(cfg *config.Config) (map[string]source.Source, error) {
	out := make(map[string]source.Source, len(cfg.Sources))
	for _, name := range sortedNames(cfg.Sources) {
		s, err := BuildSource(name, cfg.Sources[name])
		if err != nil {
			return nil, err
		}
		out[name] = s
	}
	return out, nil
}

// BuildSource constructs one source from its configuration.
func BuildSource(name string, sc config.SourceConfig) (source.Source, error) {
	raw, err := translate(name, sc)
	if err != nil {
		return nil, err
	}
	return source.New(source.Settings{ID: name, Type: string(sc.Type), Raw: raw})
}

// Converts configuration into adapter settings, resolving secrets.
func translate(name string, sc config.SourceConfig) (any, error) {
	switch {
	case sc.Postgres != nil:
		return translatePostgres(name, sc.Postgres)
	case sc.SQLite != nil:
		p := sc.SQLite
		return &source.SQLiteSettings{
			Path:                p.Path,
			BusyTimeout:         p.BusyTimeout.Duration(),
			ChangeCheckInterval: p.ChangeCheckInterval.Duration(),
		}, nil
	case sc.HTTP != nil:
		return translateHTTP(name, sc.HTTP)
	default:
		return nil, fmt.Errorf("source %q: no settings for type %q", name, sc.Type)
	}
}

func translatePostgres(name string, p *config.PostgresSource) (any, error) {
	dsn, err := p.DSN.Resolve()
	if err != nil {
		return nil, fmt.Errorf("source %q: dsn: %w", name, err)
	}
	s := &source.PostgresSettings{
		DSN:          dsn,
		MaxOpen:      p.Pool.MaxOpen,
		MaxIdle:      p.Pool.MaxIdle,
		QueryTimeout: p.QueryTimeout.Duration(),
	}
	if p.TLS != nil {
		s.TLSMode, s.TLSRootCert = p.TLS.Mode, p.TLS.RootCert
		s.TLSCert, s.TLSKey = p.TLS.Cert, p.TLS.Key
	}
	if t := p.Triggers; t != nil && t.Manage {
		ts := &source.TriggerSettings{Schema: t.Schema, CleanupOnShutdown: t.CleanupOnShutdown, DSN: dsn}
		if !t.DSN.IsZero() {
			// Provisioning normally needs privileges the collection role lacks.
			if ts.DSN, err = t.DSN.Resolve(); err != nil {
				return nil, fmt.Errorf("source %q: triggers.dsn: %w", name, err)
			}
		}
		s.Triggers = ts
	}
	return s, nil
}

func translateHTTP(name string, h *config.HTTPSource) (any, error) {
	s := &source.HTTPSettings{
		BaseURL:          h.BaseURL,
		Timeout:          h.Timeout.Duration(),
		Headers:          h.Headers,
		AllowedPaths:     h.AllowedPaths,
		MaxResponseBytes: h.MaxResponseBytes,
		MaxRedirects:     h.MaxRedirects,
		Retries:          h.Retries,
	}
	if a := h.Authentication; a != nil {
		s.AuthType = string(a.Type)
		switch a.Type {
		case config.AuthBearer:
			token, err := a.Token.Resolve()
			if err != nil {
				return nil, fmt.Errorf("source %q: authentication.token: %w", name, err)
			}
			s.Token = token
		case config.AuthBasic:
			password, err := a.Password.Resolve()
			if err != nil {
				return nil, fmt.Errorf("source %q: authentication.password: %w", name, err)
			}
			s.Username, s.Password = a.Username, password
		}
	}
	return s, nil
}

func sortedNames(m map[string]config.SourceConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
