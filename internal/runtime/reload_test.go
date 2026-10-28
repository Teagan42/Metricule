package runtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
	"github.com/teagan42/metricule/internal/source"
)

func reloadDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.Exec(fmt.Sprintf(`INSERT INTO t (id) VALUES (%d)`, i)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return path
}

func reloadConfig(t *testing.T, path, metric string) *config.Config {
	t.Helper()
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
  - name: %s
    help: Rows
    type: gauge
    source: app
    query:
      sql: SELECT COUNT(*) AS value FROM t
    mapping:
      value: value
    collect:
      mode: poll
      interval: 1h
      timeout: 5s
`, path, metric)
	cfg, ds, err := config.Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse config: %v; diagnostics: %v", err, ds)
	}
	return cfg
}

func startRuntime(t *testing.T, cfg *config.Config) (*runtime.Runtime, context.CancelFunc, <-chan error) {
	t.Helper()
	rt, err := runtime.New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for rt.Addr() == "" {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("listener never bound")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return rt, cancel, done
}

func scrapeBody(t *testing.T, addr string) string {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	return string(body)
}

func awaitMetric(t *testing.T, addr, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(scrapeBody(t, addr), "\n"+name+" ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", name)
}

func awaitMetricGone(t *testing.T, addr, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(scrapeBody(t, addr), "\n"+name+" ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s still published after reload", name)
}

func TestReloadReplacesMetricsAtomically(t *testing.T) {
	path := reloadDatabase(t)
	rt, cancel, done := startRuntime(t, reloadConfig(t, path, "old_rows"))
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop")
		}
	}()

	awaitMetric(t, rt.Addr(), "old_rows")

	next := reloadConfig(t, path, "new_rows")
	if err := rt.Reload(context.Background(), next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	awaitMetric(t, rt.Addr(), "new_rows")
	awaitMetricGone(t, rt.Addr(), "old_rows")

	resp, err := http.Get("http://" + rt.Addr() + "/readyz")
	if err != nil {
		t.Fatalf("readyz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("readyz after reload = %d", resp.StatusCode)
	}
}

// A reload that cannot open its sources must leave the running generation intact.
func TestReloadFailureLeavesTheRunningGeneration(t *testing.T) {
	path := reloadDatabase(t)
	rt, cancel, done := startRuntime(t, reloadConfig(t, path, "old_rows"))
	defer func() {
		cancel()
		<-done
	}()

	awaitMetric(t, rt.Addr(), "old_rows")

	broken := reloadConfig(t, "/definitely/absent/app.db", "new_rows")
	if err := rt.Reload(context.Background(), broken); err == nil {
		t.Fatal("reload against a missing database succeeded")
	}
	awaitMetric(t, rt.Addr(), "old_rows")
}

func TestReloadRejectsServerChanges(t *testing.T) {
	path := reloadDatabase(t)
	rt, cancel, done := startRuntime(t, reloadConfig(t, path, "old_rows"))
	defer func() {
		cancel()
		<-done
	}()

	awaitMetric(t, rt.Addr(), "old_rows")

	next := reloadConfig(t, path, "new_rows")
	next.Server.Listen = "127.0.0.1:9"
	err := rt.Reload(context.Background(), next)
	if err == nil || !strings.Contains(err.Error(), "server settings") {
		t.Fatalf("Reload error = %v, want a server-settings refusal", err)
	}
	awaitMetric(t, rt.Addr(), "old_rows")
}

// Scrapes must stay successful and race-free while generations swap.
func TestScrapesSurviveRepeatedReloads(t *testing.T) {
	path := reloadDatabase(t)
	first := reloadConfig(t, path, "first_rows")
	second := reloadConfig(t, path, "second_rows")

	rt, cancel, done := startRuntime(t, first)
	defer func() {
		cancel()
		<-done
	}()
	awaitMetric(t, rt.Addr(), "first_rows")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			resp, err := http.Get("http://" + rt.Addr() + "/metrics")
			if err != nil {
				failures <- err
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				failures <- fmt.Errorf("status %d", resp.StatusCode)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	for i := 0; i < 4; i++ {
		target := second
		if i%2 == 1 {
			target = first
		}
		if err := rt.Reload(context.Background(), target); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("reload %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("scrape during reload: %v", err)
	}
}
