package httpjson

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/teagan42/metricule/internal/source"
)

// Reserved for the authentication block. Accepting it as a static header would put a
// credential in plaintext configuration and silently override the configured scheme.
const authorizationHeader = "Authorization"

// querySpec is a compiled GET request plus its compiled selectors.
type querySpec struct {
	sourceID   string
	metricName string

	url     *url.URL
	headers http.Header

	extract *extractor
}

// Describe returns a sanitized description for diagnostics and `metricule test`.
//
// Omits header and parameter values: both come from configuration that may carry
// API keys, and this string reaches logs and CLI output (§11.3).
func (q *querySpec) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s://%s%s", q.url.Scheme, q.url.Host, q.url.Path)
	if keys := sortedKeys(q.url.Query()); len(keys) > 0 {
		fmt.Fprintf(&b, " params=[%s]", strings.Join(keys, ","))
	}
	if q.extract.rows != nil {
		fmt.Fprintf(&b, " rows=%s", q.extract.rows)
	}
	fmt.Fprintf(&b, " value=%s", q.extract.value)
	if names := q.extract.labelNames(); len(names) > 0 {
		fmt.Fprintf(&b, " labels=[%s]", strings.Join(names, ","))
	}
	return b.String()
}

// CompileQuery validates a metric's request definition and selectors.
//
// Runs at configuration compilation so an unsupported method, a path outside
// allowed_paths, or a malformed selector fails at startup, not at scrape time.
func (s *Source) CompileQuery(def source.QueryDefinition) (source.QuerySpec, error) {
	fail := func(field string, err error) (source.QuerySpec, error) {
		return nil, fmt.Errorf("metric %q source %q: %s: %w", def.MetricName, s.id, field, err)
	}

	if def.SQL != "" {
		return fail("query.sql", errors.New("an http source takes a request definition, not SQL"))
	}
	if def.Method != "" && def.Method != http.MethodGet {
		return fail("request.method", fmt.Errorf("only GET is supported in v0.1, got %q", def.Method))
	}
	u, err := s.resolvePath(def.Path)
	if err != nil {
		return fail("request.path", err)
	}
	if len(def.Params) > 0 {
		q := make(url.Values, len(def.Params))
		for k, v := range def.Params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	headers, err := mergeHeaders(s.settings.Headers, def.Headers)
	if err != nil {
		return fail("request.headers", err)
	}
	ex, err := compileExtractor(def)
	if err != nil {
		return nil, fmt.Errorf("metric %q source %q: %w", def.MetricName, s.id, err)
	}

	return &querySpec{
		sourceID:   s.id,
		metricName: def.MetricName,
		url:        u,
		headers:    headers,
		extract:    ex,
	}, nil
}

// Joins a request path onto the base URL and enforces allowed_paths.
func (s *Source) resolvePath(p string) (*url.URL, error) {
	switch {
	case p == "":
		return nil, errors.New("required field is missing")
	case !strings.HasPrefix(p, "/"):
		return nil, fmt.Errorf("path %q must begin with /", p)
	case !isCanonicalPath(p):
		// Traversal would reach outside the base path, defeating allowed_paths.
		return nil, fmt.Errorf("path %q is not canonical", p)
	}
	if err := s.checkAllowed(p); err != nil {
		return nil, err
	}
	u := *s.base
	u.Path = s.base.Path + p
	return &u, nil
}

// Rejects a path outside allowed_paths. An empty list permits any path under the
// base URL, matching the configuration contract.
func (s *Source) checkAllowed(p string) error {
	if len(s.allowed) == 0 {
		return nil
	}
	if _, ok := s.allowed[p]; ok {
		return nil
	}
	return fmt.Errorf("path %q is not in allowed_paths %v", p, sortedSet(s.allowed))
}

// Combines source-level and request-level static headers, request wins.
func mergeHeaders(sourceHeaders, requestHeaders map[string]string) (http.Header, error) {
	out := make(http.Header, len(sourceHeaders)+len(requestHeaders))
	for _, m := range []map[string]string{sourceHeaders, requestHeaders} {
		for k, v := range m {
			if http.CanonicalHeaderKey(k) == authorizationHeader {
				return nil, fmt.Errorf("header %q is not configurable; use the source authentication block", k)
			}
			out.Set(k, v)
		}
	}
	return out, nil
}

// Builds the outbound request, applying static headers then credentials.
//
// Credentials are set last and never copied anywhere else, so no code path can log
// them (§11.3, §14.3).
func (s *Source) newRequest(ctx context.Context, spec *querySpec) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.url.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range spec.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")

	switch s.settings.AuthType {
	case "":
	case authBearer:
		req.Header.Set(authorizationHeader, "Bearer "+s.settings.Token)
	case authBasic:
		req.SetBasicAuth(s.settings.Username, s.settings.Password)
	default:
		return nil, fmt.Errorf("source %q: unsupported authentication type %q", s.id, s.settings.AuthType)
	}
	return req, nil
}

// Supported authentication schemes (§11.1).
const (
	authBearer = "bearer"
	authBasic  = "basic"
)

// Allows a trailing slash, which path.Clean strips but a server may distinguish.
func isCanonicalPath(p string) bool {
	cleaned := path.Clean(p)
	return p == cleaned || p == cleaned+"/"
}

func sortedKeys(v url.Values) []string {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
