package httpjson_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// §20.4.3 — bearer and basic credentials reach the upstream in the right header.
func TestAuthentication(t *testing.T) {
	t.Parallel()

	t.Run("bearer", func(t *testing.T) {
		t.Parallel()
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"v":1}`))
		}))
		defer srv.Close()

		s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, AuthType: "bearer", Token: "s3cr3t-token"})
		query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

		if got != "Bearer s3cr3t-token" {
			t.Fatalf("Authorization = %q", got)
		}
	})

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		var gotUser, gotPass string
		var ok bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, ok = r.BasicAuth()
			_, _ = w.Write([]byte(`{"v":1}`))
		}))
		defer srv.Close()

		s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, AuthType: "basic", Username: "scraper", Password: "s3cr3t-password"})
		query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

		if !ok || gotUser != "scraper" || gotPass != "s3cr3t-password" {
			t.Fatalf("BasicAuth = %q/%q ok=%v", gotUser, gotPass, ok)
		}
	})

	t.Run("none", func(t *testing.T) {
		t.Parallel()
		var present bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, present = r.Header["Authorization"]
			_, _ = w.Write([]byte(`{"v":1}`))
		}))
		defer srv.Close()

		s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
		query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

		if present {
			t.Fatal("unauthenticated source must send no Authorization header")
		}
	})

	t.Run("unsupported scheme", func(t *testing.T) {
		t.Parallel()
		srv := jsonServer(t, `{"v":1}`)
		s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, AuthType: "oauth2"})
		if err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10); !strings.Contains(err.Error(), "oauth2") {
			t.Fatalf("error = %v", err)
		}
	})
}

// Static headers come from both the source and the request definition.
func TestStaticHeaders(t *testing.T) {
	t.Parallel()

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Headers: map[string]string{"X-Source": "src", "X-Both": "source"}})
	spec := compile(t, s, source.QueryDefinition{Value: "$.v", Headers: map[string]string{"X-Request": "req", "X-Both": "request"}})
	query(t, s, spec, 10)

	for h, want := range map[string]string{"X-Source": "src", "X-Request": "req", "X-Both": "request", "Accept": "application/json"} {
		if got.Get(h) != want {
			t.Errorf("%s = %q, want %q", h, got.Get(h), want)
		}
	}
}

// Optional query parameters reach the upstream.
func TestQueryParams(t *testing.T) {
	t.Parallel()

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Value: "$.v", Params: map[string]string{"b": "2", "a": "1 2"}})
	query(t, s, spec, 10)

	if got != "a=1+2&b=2" {
		t.Fatalf("RawQuery = %q", got)
	}
}

// A base URL with a path prefix is joined with the request path.
func TestBasePathPrefix(t *testing.T) {
	t.Parallel()

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL + "/prefix/"})
	query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v", Path: "/api/status"}), 10)

	if got != "/prefix/api/status" {
		t.Fatalf("path = %q, want /prefix/api/status", got)
	}
}

// §20.4.7 — a 5xx fails the collection and is retried a bounded number of times.
func TestServerErrorRetriedThenFails(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Retries: 2})
	err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want the status", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 + 2 retries)", got)
	}
}

// A transient 5xx that later succeeds must not fail the collection.
func TestRetrySucceeds(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"v":5}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Retries: 2})
	rs := query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if rs.Rows[0]["$.v"] != int64(5) {
		t.Fatalf("rows = %+v", rs.Rows)
	}
}

// A 4xx is a configuration or authorization fault; repeating it only adds load.
func TestClientErrorNotRetried(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Retries: 3})
	_ = queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

// §20.4.8 — the request timeout bounds a slow upstream.
func TestRequestTimeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer func() { close(release); srv.Close() }()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	start := time.Now()
	err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error = %v, want a timeout report", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %s, want the configured timeout to bound it", elapsed)
	}
}

// Collection cancellation propagates instead of being retried.
func TestContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Timeout: time.Minute, Retries: 3})
	spec := compile(t, s, source.QueryDefinition{Value: "$.v"})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	done := make(chan error, 1)
	go func() {
		_, err := s.Query(ctx, source.QueryRequest{Spec: spec, MaxRows: 10})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("error = %v, want a cancellation report", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Query ignored context cancellation")
	}
}

// §20.4.9 — an oversized body is rejected while reading.
func TestOversizedBodyRejected(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"pad":%q,"v":1}`, strings.Repeat("x", 64<<10))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, MaxResponseBytes: 1024})
	err := queryErr(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)

	if !strings.Contains(err.Error(), "max_response_bytes") {
		t.Fatalf("error = %v, want a response-limit report", err)
	}
}

// An endless body must be refused rather than buffered: the limit is enforced
// while reading, so this returns instead of exhausting memory (§11.3).
func TestEndlessBodyRejected(t *testing.T) {
	t.Parallel()

	chunk := []byte(strings.Repeat("x", 4<<10))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, MaxResponseBytes: 8 << 10, Timeout: 30 * time.Second})
	spec := compile(t, s, source.QueryDefinition{Value: "$.v"})

	done := make(chan error, 1)
	go func() {
		_, err := s.Query(context.Background(), source.QueryRequest{Spec: spec, MaxRows: 10})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
			t.Fatalf("error = %v, want a response-limit report", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an endless body was buffered instead of bounded")
	}
}

// A body exactly at the limit is accepted; the bound is inclusive.
func TestBodyAtLimitAccepted(t *testing.T) {
	t.Parallel()

	body := `{"v":1}`
	srv := jsonServer(t, body)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, MaxResponseBytes: int64(len(body))})
	query(t, s, compile(t, s, source.QueryDefinition{Value: "$.v"}), 10)
}

// §11.1 — connections are reused across collections.
func TestConnectionReuse(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	addrs := map[string]struct{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addrs[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer srv.Close()

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Value: "$.v"})
	for range 5 {
		query(t, s, spec, 10)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(addrs) != 1 {
		t.Fatalf("used %d connections, want 1 reused", len(addrs))
	}
}

// Concurrent collections share one source safely.
func TestConcurrentQueries(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[{"v":1},{"v":2}]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Rows: "$.workers[*]", Value: "$.v"})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs, err := s.Query(context.Background(), source.QueryRequest{Spec: spec, MaxRows: 10})
			if err != nil || len(rs.Rows) != 2 {
				t.Errorf("Query = %+v, %v", rs, err)
			}
		}()
	}
	wg.Wait()
}

// Health reports reachability without requesting an application path.
func TestHealth(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))

	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL, Timeout: 2 * time.Second})
	if err := s.Health(context.Background()); err != nil {
		t.Fatalf("Health on a live server: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("Health issued %d HTTP requests, want 0", got)
	}

	srv.Close()
	if err := s.Health(context.Background()); err == nil {
		t.Fatal("Health on a closed server must fail")
	}
}

// A spec from another source must not execute against this one.
func TestForeignSpecRejected(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"v":1}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})

	if _, err := s.Query(context.Background(), source.QueryRequest{Spec: otherSpec{}, MaxRows: 1}); err == nil {
		t.Fatal("expected a rejection of a foreign spec")
	}
}

type otherSpec struct{}

func (otherSpec) Describe() string { return "other" }
