package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

const watchInterval = 20 * time.Millisecond

// Builds an opened source with a short observation interval.
func openWatched(t *testing.T, path string) *Source {
	t.Helper()
	s, err := New(source.Settings{
		ID:   "app",
		Type: source.TypeSQLite,
		Raw: &source.SQLiteSettings{
			Path:                path,
			BusyTimeout:         3 * time.Second,
			ChangeCheckInterval: watchInterval,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	concrete, ok := s.(*Source)
	if !ok {
		t.Fatalf("New returned %T, want *Source", s)
	}
	if err := concrete.Open(t.Context()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = concrete.Close() })
	return concrete
}

// Starts Watch and returns its events and a stop function.
func startWatch(t *testing.T, s *Source) (<-chan source.ChangeEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan source.ChangeEvent, 32)
	done := make(chan error, 1)
	go func() { done <- s.Watch(ctx, source.WatchSpec{}, events) }()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Watch returned %v, want cancellation", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Watch did not return after cancellation")
		}
	}
	t.Cleanup(stop)
	return events, stop
}

func awaitEvent(t *testing.T, events <-chan source.ChangeEvent, want source.ChangeKind) source.ChangeEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %q event within 5s", want)
		}
	}
}

// The readiness event is what lets the scheduler reconcile after subscribing.
func TestWatchSignalsReadinessBeforeAnyChange(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openWatched(t, path)
	events, _ := startWatch(t, s)

	ev := awaitEvent(t, events, source.KindReconnect)
	if ev.SourceID != "app" {
		t.Errorf("SourceID = %q, want app", ev.SourceID)
	}
	if ev.At.IsZero() {
		t.Error("event has no timestamp")
	}
}

func TestWatchReportsACommitAsAChangeToken(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openWatched(t, path)
	events, _ := startWatch(t, s)
	awaitEvent(t, events, source.KindReconnect)

	writeTo(t, path, "INSERT INTO t VALUES (1)")

	ev := awaitEvent(t, events, source.KindToken)
	if ev.Scope != source.ScopeUnknown {
		t.Errorf("Scope = %q, want %q: data_version cannot name a table", ev.Scope, source.ScopeUnknown)
	}
}

// A write that lands before the baseline read is covered by the readiness
// reconcile, not by a token comparison.
func TestWatchReportsOneTokenPerObservedAdvance(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openWatched(t, path)
	events, _ := startWatch(t, s)
	awaitEvent(t, events, source.KindReconnect)

	writeTo(t, path, "INSERT INTO t VALUES (1)")
	awaitEvent(t, events, source.KindToken)

	// No further writes: the observer must not keep reporting change.
	select {
	case ev := <-events:
		if ev.Kind == source.KindToken {
			t.Fatalf("unexpected token with no write: %+v", ev)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// A database restored over the watched path keeps the old inode open, so the
// observer must notice the replacement and re-establish.
func TestWatchReconnectsAfterFileReplacement(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openWatched(t, path)
	events, _ := startWatch(t, s)
	awaitEvent(t, events, source.KindReconnect)

	replacement := filepath.Join(t.TempDir(), "replacement.db")
	writeTo(t, replacement, "CREATE TABLE t (v INTEGER)", "INSERT INTO t VALUES (1)")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("replace database: %v", err)
	}

	awaitEvent(t, events, source.KindReconnect)
}

func TestWatchRejectsAChannel(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openWatched(t, path)

	err := s.Watch(t.Context(), source.WatchSpec{Channel: "changes"}, make(chan source.ChangeEvent, 1))
	if err == nil {
		t.Fatal("Watch accepted a channel, want an error")
	}
}

func TestWatchReturnsPromptlyWhenCancelled(t *testing.T) {
	path := newDatabase(t, "CREATE TABLE t (v INTEGER)")
	s := openWatched(t, path)
	_, stop := startWatch(t, s)

	begin := time.Now()
	stop()
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("cancellation took %s, want under 2s", elapsed)
	}
}

// A full buffer must drop rather than stall the observer (rule 10).
func TestEmitDoesNotBlockOnAFullBuffer(t *testing.T) {
	events := make(chan source.ChangeEvent, 1)
	events <- source.ChangeEvent{Kind: source.KindToken}

	done := make(chan struct{})
	go func() {
		emit(events, source.ChangeEvent{SourceID: "app", Kind: source.KindToken})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emit blocked on a full channel")
	}
}
