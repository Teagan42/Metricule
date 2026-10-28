package main_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestServeReloadsOnSIGHUP(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, execErr := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY); INSERT INTO t (id) VALUES (1), (2);`); execErr != nil {
		t.Fatalf("seed: %v", execErr)
	}
	_ = db.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	write := func(metric string) {
		t.Helper()
		body := fmt.Sprintf(`
apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: 127.0.0.1:%d
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
`, port, dbPath, metric)
		// Replace atomically: a reload must never read a half-written document.
		tmp := filepath.Join(dir, "metricule.yaml.tmp")
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "metricule.yaml")); err != nil {
			t.Fatalf("install config: %v", err)
		}
	}
	write("before_reload")

	var stderr bytes.Buffer
	cmd := exec.Command(binary, "serve", "--config", filepath.Join(dir, "metricule.yaml"))
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	await := func(name string, present bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(base + "/metrics")
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if strings.Contains(string(body), "\n"+name+" ") == present {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s present=%v never satisfied\nstderr:\n%s", name, present, stderr.String())
	}

	await("before_reload", true)

	write("after_reload")
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("SIGHUP: %v", err)
	}

	await("after_reload", true)
	await("before_reload", false)
}
