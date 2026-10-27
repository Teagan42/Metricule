//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// Starts the change observer and returns its events.
func startWatch(t *testing.T, s source.Source) (<-chan source.ChangeEvent, context.CancelFunc) {
	t.Helper()
	watchable, ok := s.(source.WatchableSource)
	if !ok {
		t.Fatalf("%T does not implement WatchableSource", s)
	}

	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan source.ChangeEvent, 64)
	done := make(chan error, 1)
	go func() { done <- watchable.Watch(ctx, source.WatchSpec{}, events) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Watch returned %v, want cancellation", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Watch did not return after cancellation")
		}
	})
	return events, cancel
}

func awaitWatchEvent(t *testing.T, events <-chan source.ChangeEvent, want source.ChangeKind) source.ChangeEvent {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %q event within 30s", want)
		}
	}
}

// §20.3.1 through the observer: a commit from another process is a change token.
func TestWatchObservesAnExternalCommit(t *testing.T) {
	path := newDatabase(t, walMode)
	s := openSource(t, path, 3*time.Second)
	events, _ := startWatch(t, s)

	awaitWatchEvent(t, events, source.KindReconnect)

	writer := startWriter(t, path, "insert:50")
	ev := awaitWatchEvent(t, events, source.KindToken)
	writer.Wait()

	if ev.SourceID != "app" {
		t.Errorf("SourceID = %q, want app", ev.SourceID)
	}
	if ev.Scope != source.ScopeUnknown {
		t.Errorf("Scope = %q, want %q", ev.Scope, source.ScopeUnknown)
	}
}

// A burst of writes must produce at least one notification without stalling the
// observer on a full channel (rule 10).
func TestWatchSurvivesAContinuousWriter(t *testing.T) {
	path := newDatabase(t, walMode)
	s := openSource(t, path, 3*time.Second)
	events, cancel := startWatch(t, s)
	awaitWatchEvent(t, events, source.KindReconnect)

	writer := startWriter(t, path, "wal-churn:2s")
	awaitWatchEvent(t, events, source.KindToken)

	// Drain while writes continue: the observer must keep observing, not wedge.
	deadline := time.After(2 * time.Second)
	drained := 0
	for drained == 0 {
		select {
		case ev := <-events:
			if ev.Kind == source.KindToken {
				drained++
			}
		case <-deadline:
			t.Fatal("observer stalled during continuous writes")
		}
	}

	cancel()
	writer.Wait()
}

// A restored database keeps the old inode open on the observer connection, so the
// replacement surfaces as a fresh subscription instead of silence.
func TestWatchReconnectsAfterRestore(t *testing.T) {
	path := newDatabase(t, walMode)
	s := openSource(t, path, 3*time.Second)
	events, _ := startWatch(t, s)
	awaitWatchEvent(t, events, source.KindReconnect)

	replacement := filepath.Join(t.TempDir(), "restored.db")
	createDatabase(t, replacement, walMode)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("restore database: %v", err)
	}

	awaitWatchEvent(t, events, source.KindReconnect)
}
