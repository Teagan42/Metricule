package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

func TestQueryReturnsColumnsAndRows(t *testing.T) {
	path := newDatabase(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, score REAL)",
		"INSERT INTO users (name, score) VALUES ('a', 1.5), ('b', 2.5)",
	)
	s := openSource(t, path)

	rs := query(t, s, "SELECT name, score FROM users ORDER BY name", 10)

	if len(rs.Columns) != 2 || rs.Columns[0].Name != "name" || rs.Columns[1].Name != "score" {
		t.Fatalf("columns = %+v", rs.Columns)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rs.Rows)
	}
	if rs.Rows[0]["name"] != "a" || rs.Rows[0]["score"] != 1.5 {
		t.Errorf("row 0 = %+v", rs.Rows[0])
	}
	if rs.Rows[1]["name"] != "b" || rs.Rows[1]["score"] != 2.5 {
		t.Errorf("row 1 = %+v", rs.Rows[1])
	}
}

func TestQueryReportsNullAsNil(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (NULL)")
	s := openSource(t, path)

	rs := query(t, s, "SELECT v FROM t", 10)
	if got := rs.Rows[0]["v"]; got != nil {
		t.Errorf("v = %#v, want nil", got)
	}
}

// §8.7 forbids silent truncation, so an oversized result is an error.
func TestQueryFailsWhenMaxRowsIsExceeded(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1), (2), (3)")
	s := openSource(t, path)

	rs, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, "SELECT v FROM t"), MaxRows: 2})
	if !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("err = %v, want ErrTooManyRows", err)
	}
	if len(rs.Rows) != 0 {
		t.Errorf("rows = %+v, want no partial result", rs.Rows)
	}
}

func TestQueryAcceptsExactlyMaxRows(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1), (2), (3)")
	s := openSource(t, path)

	rs := query(t, s, "SELECT v FROM t", 3)
	if len(rs.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rs.Rows))
	}
}

func TestQueryTreatsNonPositiveMaxRowsAsUnbounded(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1), (2), (3)")
	s := openSource(t, path)

	rs := query(t, s, "SELECT v FROM t", 0)
	if len(rs.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rs.Rows))
	}
}

// Row is a map, so a repeated column name would discard data.
func TestQueryRejectsDuplicateColumnNames(t *testing.T) {
	s := openSource(t, newDatabase(t, "CREATE TABLE t (v INTEGER)"))

	_, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, "SELECT 1 AS n, 2 AS n"), MaxRows: 10})
	if err == nil {
		t.Fatal("Query accepted a result with duplicate column names")
	}
}

func TestQueryHonorsAnAlreadyCancelledContext(t *testing.T) {
	s := openSource(t, newDatabase(t, "CREATE TABLE t (v INTEGER)"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.Query(ctx, source.QueryRequest{Spec: compile(t, s, "SELECT count(*) FROM t"), MaxRows: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A runaway statement must be interrupted, not merely abandoned.
func TestQueryInterruptsALongRunningStatementOnDeadline(t *testing.T) {
	s := openSource(t, newDatabase(t, "CREATE TABLE t (v INTEGER)"))
	const runaway = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"
	spec := compile(t, s, runaway)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := s.Query(ctx, source.QueryRequest{Spec: spec, MaxRows: 1})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("runaway query returned without error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runaway query was not interrupted")
	}
}

func TestQueryBeforeOpenIsAnError(t *testing.T) {
	s := mustNew(t, newDatabase(t, "CREATE TABLE t (v INTEGER)"))

	_, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, "SELECT 1"), MaxRows: 1})
	if !errors.Is(err, errOpenRequired) {
		t.Fatalf("err = %v, want errOpenRequired", err)
	}
}

func TestQueryAfterCloseIsAnError(t *testing.T) {
	s := openSource(t, newDatabase(t, "CREATE TABLE t (v INTEGER)"))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, "SELECT 1"), MaxRows: 1})
	if !errors.Is(err, errClosed) {
		t.Fatalf("err = %v, want errClosed", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s := openSource(t, newDatabase(t, "CREATE TABLE t (v INTEGER)"))
	for i := range 3 {
		if err := s.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
}

func TestOpenReportsAnAbsentFile(t *testing.T) {
	s := mustNew(t, filepath.Join(t.TempDir(), "absent.db"))

	err := s.Open(t.Context())
	if err == nil {
		t.Fatal("Open accepted an absent database file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap os.ErrNotExist", err)
	}
}

// AD-007: a write must fail at the connection, not be caught by inspecting SQL.
func TestWritesAreRejectedByTheConnection(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openSource(t, path)
	before := fingerprint(t, path)

	for _, stmt := range []string{
		"INSERT INTO t VALUES (1)",
		"DELETE FROM t",
		"CREATE TABLE other (v INTEGER)",
		"UPDATE t SET v = 1",
		"DROP TABLE t",
	} {
		_, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, stmt), MaxRows: 10})
		if err == nil {
			t.Errorf("%q was accepted on a read-only connection", stmt)
		}
	}

	if after := fingerprint(t, path); after != before {
		t.Errorf("database changed: %s -> %s", before, after)
	}
}

// §10.4 forbids altering the journal mode. SQLite reports the unchanged mode
// instead of failing, so the file, not the statement result, is the assertion.
func TestJournalModeIsNotAltered(t *testing.T) {
	path := newDatabase(t, "PRAGMA journal_mode = WAL", "CREATE TABLE t (v INTEGER)")
	if mode := journalMode(t, path); mode != "wal" {
		t.Fatalf("setup journal_mode = %q, want wal", mode)
	}
	s := openSource(t, path)

	_, _ = s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, "PRAGMA journal_mode = DELETE"), MaxRows: 10})

	if mode := journalMode(t, path); mode != "wal" {
		t.Errorf("journal_mode = %q after collection, want wal", mode)
	}
}

func journalMode(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	return mode
}

func TestQueryingDoesNotModifyTheDatabase(t *testing.T) {
	path := newDatabase(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"INSERT INTO users (id) VALUES (1), (2)",
	)
	before := fingerprint(t, path)

	s := openSource(t, path)
	for range 5 {
		query(t, s, "SELECT count(*) AS n FROM users", 10)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if after := fingerprint(t, path); after != before {
		t.Errorf("database changed: %s -> %s", before, after)
	}
}

func TestReopensAfterTheFileIsReplaced(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1)")
	s := openSource(t, path)

	if n := count(t, s); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}

	replacement := filepath.Join(t.TempDir(), "replacement.db")
	writeTo(t, replacement, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1), (2), (3)")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("replace database: %v", err)
	}

	if n := count(t, s); n != 3 {
		t.Fatalf("count = %d after replacement, want 3", n)
	}
}

func TestRecoversAfterTheFileDisappearsAndReturns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	writeTo(t, path, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1)")
	s := openSource(t, path)

	if n := count(t, s); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove database: %v", err)
	}
	if _, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, "SELECT count(*) AS n FROM t"), MaxRows: 1}); err == nil {
		t.Error("Query succeeded while the database file was absent")
	}
	if err := s.Health(t.Context()); err == nil {
		t.Error("Health reported up while the database file was absent")
	}

	writeTo(t, path, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1), (2)")
	if n := count(t, s); n != 2 {
		t.Fatalf("count = %d after restore, want 2", n)
	}
	if err := s.Health(t.Context()); err != nil {
		t.Errorf("Health after restore: %v", err)
	}
}

func TestConcurrentQueriesShareOneSource(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1), (2)")
	s := openSource(t, path)
	spec := compile(t, s, "SELECT count(*) AS n FROM t")

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1})
			switch {
			case err != nil:
				errs <- err
			case rs.Rows[0]["n"] != int64(2):
				errs <- errors.New("unexpected count")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent query: %v", err)
	}
}

func newDatabase(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	writeTo(t, path, stmts...)
	return path
}

// Writes with a read-write connection: the adapter under test cannot.
func writeTo(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer db.Close()
	for _, stmt := range stmts {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

func openSource(t *testing.T, path string) source.Source {
	t.Helper()
	s := mustNew(t, path)
	if err := s.Open(t.Context()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func compile(t *testing.T, s source.Source, stmt string) source.QuerySpec {
	t.Helper()
	spec, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", SQL: stmt})
	if err != nil {
		t.Fatalf("CompileQuery(%q): %v", stmt, err)
	}
	return spec
}

func query(t *testing.T, s source.Source, stmt string, maxRows int) source.ResultSet {
	t.Helper()
	rs, err := s.Query(t.Context(), source.QueryRequest{Spec: compile(t, s, stmt), MaxRows: maxRows})
	if err != nil {
		t.Fatalf("Query(%q): %v", stmt, err)
	}
	return rs
}

func count(t *testing.T, s source.Source) int64 {
	t.Helper()
	rs := query(t, s, "SELECT count(*) AS n FROM t", 1)
	n, ok := rs.Rows[0]["n"].(int64)
	if !ok {
		t.Fatalf("count column = %#v, want int64", rs.Rows[0]["n"])
	}
	return n
}

// Fingerprints file content and mtime so an accidental write is detectable.
func fingerprint(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fmt.Sprintf("%x@%s", sha256.Sum256(data), fi.ModTime().UTC().Format(time.RFC3339Nano))
}
