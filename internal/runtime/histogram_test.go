package runtime_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

// A persisted-bucket histogram flows from SQL through mapping, the store, and the
// exporter as a real Prometheus histogram.
func TestRuntimePublishesPersistedHistogram(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE latency (route TEXT, le TEXT, bucket_count INTEGER, total_count INTEGER, total_sum REAL);
		INSERT INTO latency VALUES
			('api', '0.1', 1, 5, 3.5),
			('api', '1',   3, 5, 3.5),
			('api', '+Inf', 5, 5, 3.5);
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	doc := fmt.Sprintf(`
apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: 127.0.0.1:0
sources:
  app:
    type: sqlite
    path: %s
    busy_timeout: 3s
metrics:
  - name: app_request_duration_seconds
    help: Request duration
    type: histogram
    source: app
    query:
      sql: SELECT route, le, bucket_count AS value, total_count AS count, total_sum AS sum FROM latency
    mapping:
      value: value
      buckets: le
      count: count
      sum: sum
      labels:
        route: route
    collect:
      mode: poll
      interval: 100ms
      timeout: 5s
`, path)
	cfg, ds, err := config.Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v; diagnostics: %v", err, ds)
	}

	rt, cancel, done := startRuntime(t, cfg)
	defer func() {
		cancel()
		<-done
	}()

	waitForBody := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(scrapeBody(t, rt.Addr()), want) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("exposition never contained %q", want)
	}
	waitForBody(`app_request_duration_seconds_bucket{route="api",le="+Inf"} 5`)
	waitForBody(`app_request_duration_seconds_count{route="api"} 5`)
	waitForBody(`app_request_duration_seconds_sum{route="api"} 3.5`)
	waitForBody("# TYPE app_request_duration_seconds histogram")

	// A refresh reads the new persisted buckets; nothing is cumulated client-side.
	update, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE latency SET bucket_count = 6, total_count = 6, total_sum = 4.5 WHERE le = '+Inf'`,
		`UPDATE latency SET bucket_count = 4, total_count = 6, total_sum = 4.5 WHERE le = '1'`,
		`UPDATE latency SET bucket_count = 2, total_count = 6, total_sum = 4.5 WHERE le = '0.1'`,
	} {
		if _, err := update.Exec(stmt); err != nil {
			t.Fatalf("update %q: %v", stmt, err)
		}
	}
	_ = update.Close()

	waitForBody(`app_request_duration_seconds_bucket{route="api",le="+Inf"} 6`)
	waitForBody(`app_request_duration_seconds_count{route="api"} 6`)
}
