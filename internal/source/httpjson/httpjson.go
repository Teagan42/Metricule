// Package httpjson implements the HTTP/JSON source adapter (§11).
//
// Scope for v0.1: GET only, JSON only. SSE, WebSockets, webhooks, GraphQL,
// OAuth2 refresh, and XML are out of scope (§11.4).
//
// Credentials arrive already resolved in source.HTTPSettings and are written only
// into outbound request headers. Nothing in this package logs a header, a
// credential, or a response body (§11.3, §14.3).
//
// # Selector semantics
//
// A metric's mapping compiles into three RFC 9535 JSONPath selectors (§11.2,
// ADR 0008, ADR 0003):
//
//	rows   Optional. Selects the repeating elements of a multi-series result, such
//	       as "$.workers[*]". Empty means the document yields one row.
//	value  Required. Selects the numeric sample, such as "$.sessions.active".
//	labels Optional. Selects one label value each, such as {worker: "$.name"}.
//
// value and labels evaluate against each row node, not against the document, so
// "$" inside them refers to the row. With no rows selector the row node is the
// document, so the scalar and array forms are the same evaluation. A rows selector
// may reach any depth ("$.regions[*].pools[*]"), and may select scalars, in which
// case a value of "$" is the scalar itself.
//
// Resolved columns are keyed by the selector string itself, so the mapping engine
// performs a plain column lookup for every source type (P7). Column order is the
// value selector followed by label selectors sorted by label name, with duplicate
// selectors collapsed into one column. Column.Type reports the observed JSON type,
// or "mixed" when rows disagree; it is diagnostic only.
//
// Per-row outcomes:
//
//	one match       The converted value is stored.
//	no match        The key is omitted, which the mapper reads as a missing field.
//	null match      An explicit nil is stored, which the null policy governs (§12.4).
//	several matches An error. Choosing one would depend on JSON member ordering.
//
// Conversions: a JSON number becomes int64 when exact and float64 otherwise, a
// string becomes string, a boolean becomes bool, and null becomes nil. A
// non-finite number is rejected, as is an object or array where a scalar is
// required; neither may reach the mapper as a fabricated sample (AD-010).
//
// A rows selector that matches nothing yields a valid result with no rows. More
// rows than QueryRequest.MaxRows is an error, never a truncation (§8.7).
package httpjson

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// Fallbacks for settings a caller left zero. internal/runtime normally supplies
// these from configuration defaults; an unbounded read or an untimed request is
// never an acceptable interpretation of zero (P5).
const (
	defaultTimeout          = 5 * time.Second
	defaultMaxResponseBytes = 4 << 20
)

func init() { source.Register(source.TypeHTTP, New) }

// Source queries JSON over HTTP. Safe for concurrent use.
type Source struct {
	id       string
	base     *url.URL
	settings source.HTTPSettings
	allowed  map[string]struct{}

	clientOnce sync.Once
	httpClient *http.Client
}

// New constructs an unopened HTTP/JSON source from resolved settings.
func New(settings source.Settings) (source.Source, error) {
	cfg, ok := settings.Raw.(*source.HTTPSettings)
	if !ok {
		return nil, fmt.Errorf("source %q: expected *source.HTTPSettings, got %T", settings.ID, settings.Raw)
	}
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("source %q: base_url: %w", settings.ID, err)
	}

	normalized := *cfg
	if normalized.Timeout <= 0 {
		normalized.Timeout = defaultTimeout
	}
	if normalized.MaxResponseBytes <= 0 {
		normalized.MaxResponseBytes = defaultMaxResponseBytes
	}
	if normalized.MaxRedirects < 0 {
		normalized.MaxRedirects = 0
	}
	if normalized.Retries < 0 {
		normalized.Retries = 0
	}

	s := &Source{
		id:       settings.ID,
		base:     base,
		settings: normalized,
		allowed:  make(map[string]struct{}, len(cfg.AllowedPaths)),
	}
	for _, p := range cfg.AllowedPaths {
		s.allowed[p] = struct{}{}
	}
	return s, nil
}

// ID returns the configured source name.
func (s *Source) ID() string { return s.id }

// Type returns the adapter type identifier.
func (s *Source) Type() string { return source.TypeHTTP }

// Capabilities reports query and poll support. No change observation exists for a
// plain JSON endpoint, so notify and change-token modes stay false (§11, AD-008).
func (s *Source) Capabilities() source.Capabilities {
	return source.Capabilities{Query: true, Poll: true}
}

// Rejects a base URL that cannot be joined with a request path unambiguously, or
// that carries credentials which would then appear in every error message.
func parseBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("required field is missing")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	case u.Host == "":
		return nil, errors.New("URL has no host")
	case u.User != nil:
		return nil, errors.New("must not contain userinfo; use the authentication block")
	case u.RawQuery != "" || u.ForceQuery:
		return nil, errors.New("must not contain a query string; use request params")
	case u.Fragment != "":
		return nil, errors.New("must not contain a fragment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}
