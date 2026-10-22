package sqlite

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyDSNIsAFileURIWithReadOnlyMode(t *testing.T) {
	got, err := readOnlyDSN("/var/lib/app/app.db", 3*time.Second)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}

	// The file: prefix is load-bearing: modernc.org/sqlite strips SQLite's own
	// URI parameters from a plain filename, which would open read-write.
	if !strings.HasPrefix(got, "file:") {
		t.Fatalf("dsn %q does not use the file: URI form", got)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse dsn %q: %v", got, err)
	}
	if u.Path != "/var/lib/app/app.db" {
		t.Errorf("path = %q, want /var/lib/app/app.db", u.Path)
	}

	q := u.Query()
	if q.Get("mode") != "ro" {
		t.Errorf("mode = %q, want ro", q.Get("mode"))
	}
	if q.Get("_query_only") != "1" {
		t.Errorf("_query_only = %q, want 1", q.Get("_query_only"))
	}
	if q.Get("_busy_timeout") != "3000" {
		t.Errorf("_busy_timeout = %q, want 3000", q.Get("_busy_timeout"))
	}
}

// AD-007 forbids writes, so no parameter may make the file writable or creatable.
func TestReadOnlyDSNRejectsNothingThatCouldWrite(t *testing.T) {
	got, err := readOnlyDSN("/var/lib/app/app.db", time.Second)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}
	for _, forbidden := range []string{"_journal_mode", "_journal", "_auto_vacuum", "_vacuum", "_synchronous", "_sync", "immutable", "nolock"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("dsn %q sets %q", got, forbidden)
		}
	}
}

func TestReadOnlyDSNResolvesRelativePaths(t *testing.T) {
	got, err := readOnlyDSN("app.db", time.Second)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse dsn %q: %v", got, err)
	}
	if !filepath.IsAbs(u.Path) {
		t.Errorf("path = %q, want an absolute path", u.Path)
	}
}

// A configured path is not a compile-time constant, so it must not be able to
// smuggle driver parameters such as _pragma, which executes arbitrary SQL.
func TestReadOnlyDSNEscapesParameterDelimitersInThePath(t *testing.T) {
	const hostile = "/tmp/a.db?_pragma=journal_mode(wal)&mode=rwc"

	got, err := readOnlyDSN(hostile, time.Second)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse dsn %q: %v", got, err)
	}
	if u.Path != hostile {
		t.Errorf("path = %q, want %q", u.Path, hostile)
	}
	if q := u.Query(); q.Get("mode") != "ro" || q.Has("_pragma") {
		t.Errorf("query = %v, want mode=ro and no _pragma", q)
	}
}

func TestReadOnlyDSNRoundsSubMillisecondBusyTimeoutUp(t *testing.T) {
	got, err := readOnlyDSN("/tmp/a.db", 500*time.Microsecond)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}
	u, _ := url.Parse(got)
	if v := u.Query().Get("_busy_timeout"); v != "1" {
		t.Errorf("_busy_timeout = %q, want 1", v)
	}
}

func TestReadOnlyDSNRejectsNonPositiveBusyTimeout(t *testing.T) {
	if _, err := readOnlyDSN("/tmp/a.db", 0); err == nil {
		t.Fatal("readOnlyDSN accepted a zero busy timeout")
	}
}
