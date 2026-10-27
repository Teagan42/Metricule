//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
	pgadapter "github.com/teagan42/metricule/internal/source/postgres"
)

// Starts the listener and returns its events.
func startWatch(t *testing.T, s *pgadapter.Source, channel string, tables []string) <-chan source.ChangeEvent {
	t.Helper()
	watchable, ok := any(s).(source.WatchableSource)
	if !ok {
		t.Fatalf("%T does not implement WatchableSource", s)
	}

	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan source.ChangeEvent, 64)
	done := make(chan error, 1)
	go func() {
		done <- watchable.Watch(ctx, source.WatchSpec{Channel: channel, Tables: tables}, events)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Watch returned %v, want cancellation", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Watch did not return after cancellation")
		}
	})
	return events
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

// §20.2: a committed NOTIFY reaches the scheduler as a change event.
func TestPostgresWatchDeliversNotifications(t *testing.T) {
	dsn := sharedDSN(t, "watch_notify")
	s := openSource(t, dsn, nil)
	const channel = "metricule_watch_notify"
	events := startWatch(t, s, channel, []string{"public.jobs_watch"})

	awaitWatchEvent(t, events, source.KindReconnect)

	adminExec(t, dsn, `NOTIFY `+channel+`, 'public.jobs_watch'`)
	ev := awaitWatchEvent(t, events, source.KindNotify)
	if ev.SourceID != "appdb" {
		t.Errorf("SourceID = %q, want appdb", ev.SourceID)
	}
	if ev.Scope != "public.jobs_watch" {
		t.Errorf("Scope = %q, want public.jobs_watch", ev.Scope)
	}
	if ev.At.IsZero() {
		t.Error("event has no timestamp")
	}
}

// An undeclared payload must invalidate the whole source rather than be trusted
// as a scope filter.
func TestPostgresWatchTreatsUnknownPayloadAsUnknownScope(t *testing.T) {
	dsn := sharedDSN(t, "watch_payload")
	s := openSource(t, dsn, nil)
	const channel = "metricule_watch_payload"
	events := startWatch(t, s, channel, []string{"public.jobs_watch"})

	awaitWatchEvent(t, events, source.KindReconnect)
	adminExec(t, dsn, `NOTIFY `+channel+`, 'not-a-declared-table'`)

	ev := awaitWatchEvent(t, events, source.KindNotify)
	if ev.Scope != source.ScopeUnknown {
		t.Errorf("Scope = %q, want %q", ev.Scope, source.ScopeUnknown)
	}
}

// A lost listener must re-subscribe instead of leaving the plan on fallback
// polling forever. PostgreSQL discards notifications while disconnected, so the
// re-subscribe is announced with KindReconnect and forces reconciliation.
func TestPostgresWatchReconnectsAfterRestart(t *testing.T) {
	ctr, _ := startPostgres(t)
	proxy := startProxy(t, func() (string, error) {
		return ctr.PortEndpoint(context.Background(), "5432/tcp", "")
	})
	dsn := fmt.Sprintf("postgres://owner:owner-secret@%s/app?sslmode=disable", proxy)
	s := openSource(t, dsn, func(set *source.PostgresSettings) {
		set.QueryTimeout = 3 * time.Second
	})

	const channel = "metricule_watch_restart"
	events := startWatch(t, s, channel, []string{"public.jobs_restart"})
	awaitWatchEvent(t, events, source.KindReconnect)

	ctx := context.Background()
	stopTimeout := 30 * time.Second
	if err := ctr.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stop container: %v", err)
	}
	if err := ctr.Start(ctx); err != nil {
		t.Fatalf("start container: %v", err)
	}

	// A restart publishes a new endpoint; the proxy keeps the DSN stable.
	awaitWatchEvent(t, events, source.KindReconnect)

	// Reconnect means the subscription is live again, so NOTIFY must be delivered.
	adminExec(t, dsn, `NOTIFY `+channel+`, 'public.jobs_restart'`)
	ev := awaitWatchEvent(t, events, source.KindNotify)
	if ev.Scope != "public.jobs_restart" {
		t.Errorf("Scope = %q, want public.jobs_restart", ev.Scope)
	}

	if d := s.Diagnostics(); d.Reconnects < 1 {
		t.Errorf("Reconnects = %d, want at least 1 so the counter is observable", d.Reconnects)
	}
}
