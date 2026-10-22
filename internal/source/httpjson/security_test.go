package httpjson_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// §20.4.10 — a redirect to another origin is refused, and the credential never
// reaches the redirect target (§11.3).
func TestRedirectToDisallowedOrigin(t *testing.T) {
	t.Parallel()

	var attackerHits atomic.Int32
	var attackerSawAuth atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			attackerSawAuth.Store(true)
		}
		_, _ = w.Write([]byte(`{"v":666}`))
	}))
	defer attacker.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/api/status", http.StatusFound)
	}))
	defer upstream.Close()

	// A generous redirect budget must not permit a cross-origin hop.
	s := newSource(t, source.HTTPSettings{BaseURL: upstream.URL, AuthType: "bearer", Token: "s3cr3t-token", MaxRedirects: 5})
	err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("error = %v, want a cross-origin refusal", err)
	}
	if attackerHits.Load() != 0 {
		t.Errorf("the redirect target received %d requests, want 0", attackerHits.Load())
	}
	if attackerSawAuth.Load() {
		t.Error("the redirect target received the Authorization header")
	}
	if strings.Contains(err.Error(), "s3cr3t-token") {
		t.Error("the error message leaked the token")
	}
}

// MaxRedirects bounds same-origin hops. Zero, the default, follows nothing.
func TestSameOriginRedirects(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			http.Redirect(w, r, "/api/moved", http.StatusFound)
		case "/api/moved":
			_, _ = w.Write([]byte(`{"v":9}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// Cleanup, not defer: parallel subtests resume after this function returns.
	t.Cleanup(srv.Close)

	t.Run("followed within budget", func(t *testing.T) {
		t.Parallel()
		s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, MaxRedirects: 1})
		rs := query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)
		if rs.Rows[0]["$.v"] != int64(9) {
			t.Fatalf("rows = %+v", rs.Rows)
		}
	})

	t.Run("refused by default", func(t *testing.T) {
		t.Parallel()
		s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
		err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)
		if !strings.Contains(err.Error(), "max_redirects") {
			t.Fatalf("error = %v, want a redirect-budget refusal", err)
		}
	})
}

// A same-origin redirect cannot escape allowed_paths.
func TestRedirectOutsideAllowedPaths(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			http.Redirect(w, r, "/internal/admin", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, MaxRedirects: 3, AllowedPaths: []string{"/api/status"}})
	err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if !strings.Contains(err.Error(), "allowed_paths") {
		t.Fatalf("error = %v, want an allowed_paths refusal", err)
	}
}

// §11.3 — allowed_paths is enforced at compilation, before any request runs.
func TestAllowedPathsEnforced(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"v":1}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, AllowedPaths: []string{"/api/status", "/api/health"}})

	if _, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", Path: "/api/secrets", Value: "$.v"}); err == nil {
		t.Fatal("expected a rejection of a path outside allowed_paths")
	}
	for _, p := range []string{"/api/status", "/api/health"} {
		if _, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", Path: p, Value: "$.v"}); err != nil {
			t.Errorf("CompileQuery(%q): %v", p, err)
		}
	}
}

// An empty allowed_paths permits any path under the base URL.
func TestEmptyAllowedPathsPermitsAny(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"v":1}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	if _, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", Path: "/anything", Value: "$.v"}); err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
}

func TestCompileRejections(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"v":1}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})

	for name, def := range map[string]source.QueryDefinition{
		"post method":           {Method: http.MethodPost, Path: "/api/status", Value: "$.v"},
		"sql on an http source": {SQL: "SELECT 1", Path: "/api/status", Value: "$.v"},
		"missing path":          {Value: "$.v"},
		"relative path":         {Path: "api/status", Value: "$.v"},
		"traversal path":        {Path: "/api/../../etc/passwd", Value: "$.v"},
		"missing value":         {Path: "/api/status"},
		"invalid value":         {Path: "/api/status", Value: "$["},
		"invalid rows":          {Path: "/api/status", Rows: "$[", Value: "$.v"},
		"invalid label":         {Path: "/api/status", Value: "$.v", Labels: map[string]string{"w": "$["}},
		"authorization header":  {Path: "/api/status", Value: "$.v", Headers: map[string]string{"authorization": "Bearer x"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			def.MetricName = "app_metric"
			if _, err := s.CompileQuery(def); err == nil {
				t.Fatalf("expected a rejection of %+v", def)
			}
		})
	}
}

// Explicit GET is accepted; an empty method defaults to GET.
func TestExplicitGetAccepted(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"v":1}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	if _, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", Method: http.MethodGet, Path: "/api/status", Value: "$.v"}); err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
}

// §11.3, §14.3 — no credential appears in log output, error text, or Describe.
func TestNoCredentialInLogsOrErrors(t *testing.T) {
	// Not parallel: slog.SetDefault is process-wide.
	const (
		token    = "tok-AAAAAAAAAAAAAAAA"
		password = "pw-BBBBBBBBBBBBBBBB"
		paramKey = "key-CCCCCCCCCCCCCCCC"
	)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	// A 500 drives the retry log path; the error body echoes the credential so a
	// response-body leak would show up too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"echo":"` + r.Header.Get("Authorization") + `"}`))
	}))
	defer srv.Close()

	var errs []string
	for _, cfg := range []source.HTTPSettings{
		{BaseURL: srv.URL, AuthType: "bearer", Token: token, Retries: 1, Timeout: time.Second},
		{BaseURL: srv.URL, AuthType: "basic", Username: "scraper", Password: password, Retries: 1, Timeout: time.Second},
	} {
		s := newSource(t, cfg)
		spec := compile(t, s, source.QueryDefinition{Value: "$.v", Params: map[string]string{"api_key": paramKey}})
		errs = append(errs, spec.Describe())
		_, err := s.Query(context.Background(), source.QueryRequest{Spec: spec, MaxRows: 10})
		if err == nil {
			t.Fatal("expected the collection to fail")
		}
		errs = append(errs, err.Error())
		if err := s.Health(context.Background()); err != nil {
			errs = append(errs, err.Error())
		}
	}

	haystack := logs.String() + "\n" + strings.Join(errs, "\n")
	if logs.Len() == 0 {
		t.Fatal("no log output captured; the leak check proved nothing")
	}
	for _, secret := range []string{token, password, paramKey} {
		if strings.Contains(haystack, secret) {
			t.Errorf("credential %q appeared in log or error output:\n%s", secret, haystack)
		}
	}
	// Basic auth encodes the credential; check the wire form too.
	if strings.Contains(haystack, "c2NyYXBlcjpwdy1C") {
		t.Error("base64 basic credential appeared in log or error output")
	}
}
