//go:build integration

package integration

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"

	// Registers the adapter under test with the shared registry.
	_ "github.com/teagan42/metricule/internal/source/sqlite"
)

const schema = "CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY, seq INTEGER)"

// §20.3.1: a commit from another process becomes visible to collection.
func TestExternalWriterCommitsBecomeVisible(t *testing.T) {
	path := newDatabase(t, walMode)
	s := openSource(t, path, 3*time.Second)

	if n := count(t, s); n != 0 {
		t.Fatalf("initial count = %d, want 0", n)
	}

	writer := startWriter(t, path, "insert:200")
	writer.Wait()

	if n := awaitCount(t, s, 200, 30*time.Second); n != 200 {
		t.Fatalf("count = %d after external writer, want 200", n)
	}
}

// §20.3.4: an existing WAL database with a live writer stays readable.
func TestWALDatabaseWithALiveExternalWriter(t *testing.T) {
	path := newDatabase(t, walMode)
	writer := startWriter(t, path, "wal-churn:5s")

	// The sidecar may start after the application has an active WAL.
	awaitFile(t, path+"-wal", 10*time.Second)
	s := openSource(t, path, 3*time.Second)

	if mode := journalMode(t, path); mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	var last int64
	for range 20 {
		n := count(t, s)
		if n < last {
			t.Fatalf("count went backwards: %d then %d", last, n)
		}
		last = n
		time.Sleep(50 * time.Millisecond)
	}
	if last == 0 {
		t.Fatal("no committed rows were observed")
	}
	writer.Wait()
}

// §20.3.5: concurrent collections against a concurrently written database.
func TestConcurrentCollectionsDuringExternalWrites(t *testing.T) {
	path := newDatabase(t, walMode)
	writer := startWriter(t, path, "wal-churn:5s")
	s := openSource(t, path, 3*time.Second)
	spec := compile(t, s, "SELECT count(*) AS n FROM events")

	var wg sync.WaitGroup
	errs := make(chan error, 256)
	deadline := time.Now().Add(3 * time.Second)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if _, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent collection: %v", err)
	}
	writer.Wait()
}

// §20.3.6: a locked database waits for the busy timeout, then fails visibly and
// recovers once the lock is released. Rollback-journal mode, because WAL readers
// are not blocked by a writer.
func TestBusyDatabaseHonorsTheBusyTimeout(t *testing.T) {
	const busyTimeout = 750 * time.Millisecond

	path := newDatabase(t, rollbackMode)
	s := openSource(t, path, busyTimeout)
	writer := startWriter(t, path, "hold-exclusive:3s")

	spec := compile(t, s, "SELECT count(*) AS n FROM events")
	start := time.Now()
	_, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("collection succeeded while the database was exclusively locked")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
		t.Errorf("err = %v, want a busy or locked error", err)
	}
	// Waiting proves busy_timeout reached the connection instead of failing fast.
	if elapsed < busyTimeout/2 {
		t.Errorf("failed after %s, want at least %s of waiting", elapsed, busyTimeout/2)
	}

	writer.Wait()

	// §20.3.10: collection reconciles once the source is usable again.
	if n := awaitCount(t, s, 0, 10*time.Second); n != 0 {
		t.Fatalf("count = %d after the lock was released, want 0 (the writer rolled back)", n)
	}
}

// §20.3.7 and §20.3.10: removal fails visibly and restoration recovers.
func TestFileRemovalAndRestoration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	createDatabase(t, path, walMode)
	s := openSource(t, path, time.Second)
	spec := compile(t, s, "SELECT count(*) AS n FROM events")

	for cycle := range 3 {
		if _, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1}); err != nil {
			t.Fatalf("cycle %d: query before removal: %v", cycle, err)
		}

		removeDatabase(t, path)
		_, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1})
		if err == nil {
			t.Fatalf("cycle %d: query succeeded with the file removed", cycle)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cycle %d: err = %v, want it to wrap os.ErrNotExist", cycle, err)
		}
		if err := s.Health(t.Context()); err == nil {
			t.Errorf("cycle %d: Health reported up with the file removed", cycle)
		}

		createDatabase(t, path, walMode)
		if _, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1}); err != nil {
			t.Fatalf("cycle %d: query after restore: %v", cycle, err)
		}
		if err := s.Health(t.Context()); err != nil {
			t.Fatalf("cycle %d: Health after restore: %v", cycle, err)
		}
	}
}

// §20.3.8: an atomic replacement is detected and reopened.
func TestFileReplacementIsDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	createDatabase(t, path, walMode)
	seed(t, path, 2)
	s := openSource(t, path, time.Second)

	if n := count(t, s); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}

	staging := filepath.Join(dir, "staged.db")
	createDatabase(t, staging, walMode)
	seed(t, staging, 7)
	checkpoint(t, staging)
	if err := os.Rename(staging, path); err != nil {
		t.Fatalf("replace database: %v", err)
	}
	// A rename leaves the predecessor's sidecars behind; a real deployment that
	// swaps a WAL database ships them too.
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	if err := os.Rename(staging+"-wal", path+"-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move wal: %v", err)
	}

	if n := awaitCount(t, s, 7, 10*time.Second); n != 7 {
		t.Fatalf("count = %d after replacement, want 7", n)
	}
}

// §20.3.9: an unreadable file produces an actionable error, not a hang.
func TestPermissionDeniedIsReportedActionably(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	path := newDatabase(t, walMode)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	s, err := source.New(settings(path, time.Second))
	if err != nil {
		t.Fatalf("source.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	err = s.Open(t.Context())
	if err == nil {
		t.Fatal("Open succeeded on an unreadable file")
	}
	// Operators hit this through a volume mount, so the message must say so.
	if !strings.Contains(err.Error(), "65532") {
		t.Errorf("err = %v, want it to name the exporter uid", err)
	}
}

// AD-007, mechanically checked: a full collection cycle leaves the database file
// byte-identical with an unchanged modification time, and adds nothing to the
// write-ahead log.
//
// SQLite itself creates an empty -wal and a -shm WAL index beside a WAL database
// on first read, read-only connection or not, because WAL readers coordinate
// through shared memory. That is upstream behavior and not a change to
// application data, so the assertion is on the database file and on WAL content.
func TestCollectionCycleDoesNotModifyTheDatabase(t *testing.T) {
	path := newDatabase(t, walMode)
	seed(t, path, 25)
	checkpoint(t, path)

	before := fingerprint(t, path)

	s := openSource(t, path, 3*time.Second)
	spec := compile(t, s, "SELECT seq FROM events ORDER BY seq")
	for range 10 {
		if _, err := s.Query(t.Context(), source.QueryRequest{Spec: spec, MaxRows: 1000}); err != nil {
			t.Fatalf("collect: %v", err)
		}
		if err := s.Health(t.Context()); err != nil {
			t.Fatalf("health: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if after := fingerprint(t, path); after != before {
		t.Errorf("collection modified the database\nbefore: %s\nafter:  %s", before, after)
	}
	if n := sizeOf(t, path+"-wal"); n != 0 {
		t.Errorf("collection left %d bytes in the write-ahead log, want 0", n)
	}
	if mode := journalMode(t, path); mode != "wal" {
		t.Errorf("journal_mode = %q after collection, want wal", mode)
	}
}

// §20.3.3, characterization only. M1 implements no change observer: the adapter
// does not implement source.WatchableSource. This records the invariant the M2
// observer depends on, so a driver upgrade that breaks it fails here first.
func TestDataVersionIsOnlyComparableWithinOneConnection(t *testing.T) {
	path := newDatabase(t, walMode)

	observer := openReadOnly(t, path)
	first := dataVersion(t, observer)

	writer := startWriter(t, path, "insert:5")
	writer.Wait()

	if second := dataVersion(t, observer); second == first {
		t.Errorf("data_version did not advance after an external commit: %d", second)
	}

	// A fresh connection starts its own sequence, which is why §10.2 forbids
	// comparing readings across connections.
	other := openReadOnly(t, path)
	a, b := dataVersion(t, observer), dataVersion(t, other)
	if a == b {
		t.Logf("independent connections happened to agree (%d); the values are still not comparable", a)
	}
}

// Names the fixture for the two-user CI step. See the sqlite job in
// .github/workflows/integration.yaml, which cannot be expressed inside one test
// because switching uid needs sudo.
const sharedDBEnv = "METRICULE_TEST_SHARED_DB"

// TestSharedVolumeFixture creates the fixture as the owning user, not a test.
func TestSharedVolumeFixture(t *testing.T) {
	path := os.Getenv(sharedDBEnv)
	if path == "" {
		t.Skip("fixture role; driven by the sqlite CI job")
	}
	// Rollback-journal mode: a read-only bind mount cannot host the WAL sidecars.
	createDatabase(t, path, rollbackMode)
	seed(t, path, 5)
}

// §14.1 and §16: the published image runs as uid 65532 and must collect from a
// database file owned by the monitored application.
func TestSharedVolumeCollection(t *testing.T) {
	path := os.Getenv(sharedDBEnv)
	if path == "" {
		t.Skip("shared-volume role; driven by the sqlite CI job")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == os.Geteuid() {
		t.Fatalf("fixture is owned by the collecting user (uid %d); the test proves nothing", st.Uid)
	}

	before := fingerprint(t, path)
	s := openSource(t, path, time.Second)
	if n := count(t, s); n != 5 {
		t.Fatalf("count = %d, want 5", n)
	}
	if after := fingerprint(t, path); after != before {
		t.Errorf("collection modified a file it does not own\nbefore: %s\nafter:  %s", before, after)
	}
}

const (
	walMode      = "PRAGMA journal_mode = WAL"
	rollbackMode = "PRAGMA journal_mode = DELETE"
)

func settings(path string, busyTimeout time.Duration) source.Settings {
	return source.Settings{
		ID:   "app",
		Type: source.TypeSQLite,
		Raw: &source.SQLiteSettings{
			Path:                path,
			BusyTimeout:         busyTimeout,
			ChangeCheckInterval: 100 * time.Millisecond,
		},
	}
}

func newDatabase(t *testing.T, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	createDatabase(t, path, mode)
	return path
}

func createDatabase(t *testing.T, path, mode string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer db.Close()
	for _, stmt := range []string{mode, schema} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

func seed(t *testing.T, path string, n int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	for i := range n {
		if _, err := db.ExecContext(t.Context(), "INSERT INTO events (seq) VALUES (?)", i); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

// Folds the WAL back into the main file so a fingerprint is stable.
func checkpoint(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

func removeDatabase(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove %s: %v", path+suffix, err)
		}
	}
}

func openSource(t *testing.T, path string, busyTimeout time.Duration) source.Source {
	t.Helper()
	s, err := source.New(settings(path, busyTimeout))
	if err != nil {
		t.Fatalf("source.New: %v", err)
	}
	if err := s.Open(t.Context()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func compile(t *testing.T, s source.Source, stmt string) source.QuerySpec {
	t.Helper()
	spec, err := s.CompileQuery(source.QueryDefinition{MetricName: "events_total", SQL: stmt})
	if err != nil {
		t.Fatalf("CompileQuery(%q): %v", stmt, err)
	}
	return spec
}

func count(t *testing.T, s source.Source) int64 {
	t.Helper()
	rs, err := s.Query(t.Context(), source.QueryRequest{
		Spec:    compile(t, s, "SELECT count(*) AS n FROM events"),
		MaxRows: 1,
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	n, ok := rs.Rows[0]["n"].(int64)
	if !ok {
		t.Fatalf("count column = %#v, want int64", rs.Rows[0]["n"])
	}
	return n
}

// Polls until the collected count reaches want, mirroring the scheduler's
// eventual-consistency contract (§21 scenario B).
func awaitCount(t *testing.T, s source.Source, want int64, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int64
	for {
		last = count(t, s)
		if last == want || time.Now().After(deadline) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func awaitFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs %s: %v", path, err)
	}
	db, err := sql.Open("sqlite", "file://"+abs+"?mode=ro")
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func dataVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(t.Context(), "PRAGMA data_version").Scan(&v); err != nil {
		t.Fatalf("read data_version: %v", err)
	}
	return v
}

func journalMode(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	return mode
}

func sizeOf(t *testing.T, name string) int64 {
	t.Helper()
	fi, err := os.Stat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return 0
	case err != nil:
		t.Fatalf("stat %s: %v", name, err)
	}
	return fi.Size()
}

// Fingerprints a file by content hash and modification time.
func fingerprint(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "absent"
	case err != nil:
		t.Fatalf("read %s: %v", name, err)
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return fmt.Sprintf("%x@%s", sha256.Sum256(data), fi.ModTime().UTC().Format(time.RFC3339Nano))
}
