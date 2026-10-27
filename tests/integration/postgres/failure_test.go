//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
)

// Failure injection: a source outage must not make /metrics slow, fail, or
// fabricate a zero, and recovery must refresh without a restart (P2, P3, P6).
func TestRuntimeServesStaleAndRecoversAcrossAnOutage(t *testing.T) {
	ctr, _ := startPostgres(t)
	proxy := startProxy(t, func() (string, error) {
		return ctr.PortEndpoint(context.Background(), "5432/tcp", "")
	})
	dsn := fmt.Sprintf("postgres://owner:owner-secret@%s/app?sslmode=disable", proxy)

	u := uniqueSuffix()
	table := "jobs_failure_" + u
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS public.`+table,
		`CREATE TABLE public.`+table+` (id bigserial PRIMARY KEY)`,
		`INSERT INTO public.`+table+` DEFAULT VALUES`,
		`INSERT INTO public.`+table+` DEFAULT VALUES`,
		`INSERT INTO public.`+table+` DEFAULT VALUES`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn, `DROP TABLE IF EXISTS public.`+table)
	})

	port := freePort(t)
	url := "http://127.0.0.1:" + port
	cfgPath := writeFailureConfig(t, fmt.Sprintf(`
apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: 127.0.0.1:%s
defaults:
  failure:
    mode: retain
    max_stale_age: 5m
sources:
  appdb:
    type: postgres
    dsn:
      value: %q
metrics:
  - name: app_jobs
    help: Rows in the outage table
    type: gauge
    source: appdb
    query:
      sql: SELECT COUNT(*) AS value FROM public.%s
    mapping:
      value: value
    collect:
      mode: poll
      interval: 200ms
      timeout: 2s
`, port, dsn, table))

	cfg, ds, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v; warnings: %v", err, ds)
	}
	rt, err := runtime.New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("Runtime.Run did not return")
		}
	}()

	awaitMetric(t, url, "app_jobs", "3", 30*time.Second)

	stopTimeout := 30 * time.Second
	if err := ctr.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop container: %v", err)
	}

	// Scrapes keep succeeding, keep the last good value, and stay fast while the
	// source is gone.
	var worst time.Duration
	for range 5 {
		body, elapsed, err := scrape(url)
		if err != nil {
			t.Fatalf("scrape during outage: %v", err)
		}
		if elapsed > worst {
			worst = elapsed
		}
		if v, ok := metricValue(body, "app_jobs"); !ok || v != "3" {
			t.Fatalf("app_jobs = %q (found=%v) during outage, want the retained 3\n%s", v, ok, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if worst > 500*time.Millisecond {
		t.Errorf("worst scrape during outage = %s, want under 500ms: scraping must not wait on collection", worst)
	}
	if code, err := getStatus(t, url+"/readyz"); err != nil || code != http.StatusOK {
		t.Errorf("readyz during outage = %d, %v; readiness covers configuration, not upstream health", code, err)
	}

	// Collection failures must be visible, not silent.
	errPattern := regexp.MustCompile(`metricule_collection_errors_total\{[^}]*\} [1-9][0-9]*`)
	if err := waitFor(15*time.Second, func() error {
		body, _, err := scrape(url)
		if err != nil {
			return err
		}
		if !errPattern.MatchString(body) {
			return fmt.Errorf("collection error counter not incremented yet")
		}
		return nil
	}); err != nil {
		t.Fatalf("failure telemetry: %v", err)
	}

	if err := ctr.Start(context.Background()); err != nil {
		t.Fatalf("restart container: %v", err)
	}
	if err := waitFor(pgRecoveryBudget, func() error { return pingDSN(dsn) }); err != nil {
		t.Fatalf("database did not come back: %v", err)
	}
	adminExec(t, dsn, `INSERT INTO public.`+table+` DEFAULT VALUES`)

	awaitMetric(t, url, "app_jobs", "4", pgRecoveryBudget)
}

// A slow query must not block scrapes or publish a fabricated value (P2).
func TestScrapesStayFastDuringASlowCollection(t *testing.T) {
	ctr, _ := startPostgres(t)
	proxy := startProxy(t, func() (string, error) {
		return ctr.PortEndpoint(context.Background(), "5432/tcp", "")
	})
	dsn := fmt.Sprintf("postgres://owner:owner-secret@%s/app?sslmode=disable", proxy)

	u := uniqueSuffix()
	table := "jobs_slow_" + u
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS public.`+table,
		`CREATE TABLE public.`+table+` (id bigserial PRIMARY KEY)`,
		`INSERT INTO public.`+table+` DEFAULT VALUES`,
		`INSERT INTO public.`+table+` DEFAULT VALUES`,
		`INSERT INTO public.`+table+` DEFAULT VALUES`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn, `DROP TABLE IF EXISTS public.`+table)
	})

	port := freePort(t)
	url := "http://127.0.0.1:" + port
	cfgPath := writeFailureConfig(t, fmt.Sprintf(`
apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: 127.0.0.1:%s
sources:
  appdb:
    type: postgres
    dsn:
      value: %q
metrics:
  - name: app_slow
    help: Rows, deliberately collected slowly
    type: gauge
    source: appdb
    query:
      sql: SELECT COUNT(*) AS value FROM public.%s CROSS JOIN (SELECT pg_sleep(0.75)) AS s
    mapping:
      value: value
    collect:
      mode: poll
      interval: 100ms
      timeout: 5s
`, port, dsn, table))

	cfg, ds, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v; warnings: %v", err, ds)
	}
	rt, err := runtime.New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("Runtime.Run did not return")
		}
	}()

	awaitMetric(t, url, "app_slow", "3", 30*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	var worst time.Duration
	for time.Now().Before(deadline) {
		body, elapsed, err := scrape(url)
		if err != nil {
			t.Fatalf("scrape during slow collection: %v", err)
		}
		if elapsed > worst {
			worst = elapsed
		}
		if v, ok := metricValue(body, "app_slow"); !ok || v != "3" {
			t.Fatalf("app_slow = %q (found=%v) during a slow collection\n%s", v, ok, body)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if worst > 300*time.Millisecond {
		t.Errorf("worst scrape during a slow collection = %s, want under 300ms", worst)
	}
}

func writeFailureConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metricule.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// GET /metrics and return the body and wall time. Errors are returned rather than
// failing so readiness polling can retry while the listener binds.
func scrape(base string) (string, time.Duration, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	started := time.Now()
	resp, err := client.Get(base + "/metrics")
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	return string(body), time.Since(started), nil
}

func getStatus(t *testing.T, url string) (int, error) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// Reads a single-series family from the exposition text.
func metricValue(body, name string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 {
			return fields[1], true
		}
	}
	return "", false
}

func awaitMetric(t *testing.T, base, name, want string, budget time.Duration) {
	t.Helper()
	if err := waitFor(budget, func() error {
		body, _, err := scrape(base)
		if err != nil {
			return err
		}
		got, ok := metricValue(body, name)
		if !ok {
			return fmt.Errorf("%s not published yet", name)
		}
		if got != want {
			return fmt.Errorf("%s = %s, want %s", name, got, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("%s never reached %s: %v", name, want, err)
	}
}

func pingDSN(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.Ping(ctx)
}
