//go:build integration

package integration

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
	"syscall"
	"testing"
	"time"
)

// Process startup: binary launch to a ready /readyz, including source open and the
// first collection attempt (§20.6).
func BenchmarkStartupToReady(b *testing.B) {
	bin := buildBinary(b)

	for range b.N {
		dir := b.TempDir()
		dbPath := filepath.Join(dir, "app.db")
		createSQLiteDB(b, dbPath)

		port := freePort(b)
		cfgPath := filepath.Join(dir, "metricule.yaml")
		writeFile(b, cfgPath, fmt.Sprintf(`
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
  - name: app_rows
    help: Rows in the benchmark table
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
`, port, dbPath))

		cmd := exec.Command(bin, "serve", "--config", cfgPath)
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr

		started := time.Now()
		if err := cmd.Start(); err != nil {
			b.Fatalf("start: %v", err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		if err := waitReady(port, exited); err != nil {
			_ = cmd.Process.Kill()
			<-exited
			b.Fatalf("startup: %v\nstderr:\n%s", err, stderr.String())
		}
		b.ReportMetric(float64(time.Since(started).Nanoseconds()), "ns/startup")

		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			b.Fatalf("signal: %v", err)
		}
		select {
		case err := <-exited:
			if err != nil {
				b.Fatalf("serve exited after SIGTERM: %v\n%s", err, stderr.String())
			}
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			b.Fatal("serve did not exit after SIGTERM")
		}
	}
}

func buildBinary(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	bin := filepath.Join(dir, "metricule")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/metricule")
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("build metricule: %v\n%s", err, out)
	}
	return bin
}

func createSQLiteDB(b *testing.B, path string) {
	b.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		b.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (id) VALUES (1), (2), (3)`); err != nil {
		b.Fatalf("seed: %v", err)
	}
}

func writeFile(b *testing.B, path, body string) {
	b.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		b.Fatalf("write %s: %v", path, err)
	}
}

func freePort(b *testing.B) int {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitReady(port int, exited <-chan error) error {
	deadline := time.Now().Add(20 * time.Second)
	url := fmt.Sprintf("http://127.0.0.1:%d/readyz", port)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			if err == nil {
				return fmt.Errorf("process exited before becoming ready")
			}
			return fmt.Errorf("process exited: %w", err)
		default:
		}

		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("not ready within 20s")
}
