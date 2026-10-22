//go:build integration

// Package integration exercises the SQLite adapter against real database files
// written by a real concurrent process. Rule 9 forbids mocking a datastore whose
// own behavior — WAL visibility, locking, replacement — is what is under test.
package integration

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	// Read-write driver for the test fixtures. The adapter under test can only read.
	_ "modernc.org/sqlite"
)

const (
	// Selects the writer subprocess role when the test binary re-executes itself.
	writerModeEnv = "METRICULE_TEST_WRITER_MODE"
	writerPathEnv = "METRICULE_TEST_WRITER_PATH"

	writerReady = "WRITER-READY"
)

// Writer is an external process that holds and mutates the database file.
type Writer struct {
	t   *testing.T
	cmd *exec.Cmd
	log *os.File
}

// startWriter re-executes the test binary as a separate OS process.
//
// A goroutine in this process would share SQLite's in-process locking state and
// would not prove cross-process behavior.
func startWriter(t *testing.T, path, mode string) *Writer {
	t.Helper()

	logPath := fmt.Sprintf("%s/writer-%s.log", t.TempDir(), strings.ReplaceAll(mode, ":", "-"))
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create writer log: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWriterSubprocess$", "-test.v")
	cmd.Env = append(os.Environ(), writerModeEnv+"="+mode, writerPathEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("writer stdout: %v", err)
	}
	cmd.Stderr = log

	if err := cmd.Start(); err != nil {
		t.Fatalf("start writer: %v", err)
	}
	w := &Writer{t: t, cmd: cmd, log: log}
	t.Cleanup(func() { w.stop() })

	// Forwarding to the log file keeps the pipe drained; a blocked writer would
	// otherwise look like a lock timeout.
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		seen := false
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(log, line)
			if !seen && strings.Contains(line, writerReady) {
				seen = true
				close(ready)
			}
		}
		if !seen {
			close(ready)
		}
	}()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("writer did not become ready")
	}
	return w
}

// Wait blocks until the writer exits, failing the test on a non-zero status.
func (w *Writer) Wait() {
	w.t.Helper()
	if err := w.cmd.Wait(); err != nil {
		w.t.Fatalf("writer failed: %v\n%s", err, w.output())
	}
	w.cmd = nil
}

func (w *Writer) stop() {
	if w.cmd == nil || w.cmd.Process == nil {
		return
	}
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
}

func (w *Writer) output() string {
	data, err := os.ReadFile(w.log.Name())
	if err != nil {
		return "(writer log unavailable: " + err.Error() + ")"
	}
	return string(data)
}

// TestWriterSubprocess is the writer role of this test binary, not a test.
//
// It skips unless the parent selected a mode, so a plain run reports it skipped.
func TestWriterSubprocess(t *testing.T) {
	mode := os.Getenv(writerModeEnv)
	if mode == "" {
		t.Skip("writer subprocess role; driven by startWriter")
	}
	path := os.Getenv(writerPathEnv)

	name, arg, _ := strings.Cut(mode, ":")
	switch name {
	case "insert":
		n, err := strconv.Atoi(arg)
		if err != nil {
			t.Fatalf("insert count %q: %v", arg, err)
		}
		insertRows(t, path, n)
	case "hold-exclusive":
		d, err := time.ParseDuration(arg)
		if err != nil {
			t.Fatalf("hold duration %q: %v", arg, err)
		}
		holdExclusive(t, path, d)
	case "wal-churn":
		d, err := time.ParseDuration(arg)
		if err != nil {
			t.Fatalf("churn duration %q: %v", arg, err)
		}
		churn(t, path, d)
	default:
		t.Fatalf("unknown writer mode %q", mode)
	}
}

func insertRows(t *testing.T, path string, n int) {
	t.Helper()
	db := openWriter(t, path)
	defer db.Close()

	announceReady(t)
	for i := range n {
		if _, err := db.Exec("INSERT INTO events (seq) VALUES (?)", i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
}

// Blocks readers: an EXCLUSIVE transaction on a rollback-journal database makes
// every read return SQLITE_BUSY until it is released.
func holdExclusive(t *testing.T, path string, d time.Duration) {
	t.Helper()
	db := openWriter(t, path)
	defer db.Close()

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("dedicated connection: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("begin exclusive: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO events (seq) VALUES (-1)"); err != nil {
		t.Fatalf("write inside exclusive transaction: %v", err)
	}

	announceReady(t)
	time.Sleep(d)

	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

// Keeps committing for d so the -wal and -shm files stay present and active.
func churn(t *testing.T, path string, d time.Duration) {
	t.Helper()
	db := openWriter(t, path)
	defer db.Close()

	announceReady(t)
	deadline := time.Now().Add(d)
	for seq := 0; time.Now().Before(deadline); seq++ {
		if _, err := db.Exec("INSERT INTO events (seq) VALUES (?)", seq); err != nil {
			t.Fatalf("insert %d: %v", seq, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func openWriter(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping writer: %v", err)
	}
	return db
}

func announceReady(t *testing.T) {
	t.Helper()
	fmt.Println(writerReady)
	_ = os.Stdout.Sync()
}
