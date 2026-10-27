package runtime

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

// Minimal WatchableSource. Tests supply only the behavior they exercise.
type fakeWatchable struct {
	id    string
	watch func(context.Context, source.WatchSpec, chan<- source.ChangeEvent) error
}

func (f *fakeWatchable) ID() string   { return f.id }
func (f *fakeWatchable) Type() string { return "fake" }
func (f *fakeWatchable) Capabilities() source.Capabilities {
	return source.Capabilities{Query: true, Poll: true, Notifications: true}
}
func (f *fakeWatchable) Open(context.Context) error   { return nil }
func (f *fakeWatchable) Health(context.Context) error { return nil }
func (f *fakeWatchable) Close() error                 { return nil }
func (f *fakeWatchable) CompileQuery(source.QueryDefinition) (source.QuerySpec, error) {
	return nil, nil
}
func (f *fakeWatchable) Query(context.Context, source.QueryRequest) (source.ResultSet, error) {
	return source.ResultSet{}, nil
}
func (f *fakeWatchable) Watch(ctx context.Context, spec source.WatchSpec, events chan<- source.ChangeEvent) error {
	return f.watch(ctx, spec, events)
}

func TestBuildWatchGroupsMergesScopesOnOneChannel(t *testing.T) {
	src := &fakeWatchable{id: "app"}
	sources := map[string]source.Source{"app": src}
	plans := []compiled{
		{plan: collect.Plan{
			ID: "a", SourceID: "app", Mode: config.ModeNotify,
			Watch: &source.WatchSpec{Channel: "changes", Tables: []string{"jobs"}, Events: []string{"insert"}},
		}},
		{plan: collect.Plan{
			ID: "b", SourceID: "app", Mode: config.ModeAuto,
			Watch: &source.WatchSpec{Channel: "changes", Tables: []string{"jobs", "users"}, Events: []string{"update", "insert"}},
		}},
	}

	groups := buildWatchGroups(plans, sources)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.channel != "changes" {
		t.Errorf("channel = %q, want changes", g.channel)
	}
	if got := g.spec.Tables; len(got) != 2 || got[0] != "jobs" || got[1] != "users" {
		t.Errorf("tables = %v, want [jobs users]", got)
	}
	if got := g.spec.Events; len(got) != 2 || got[0] != "insert" || got[1] != "update" {
		t.Errorf("events = %v, want [insert update]", got)
	}
}

func TestBuildWatchGroupsSeparatesChannels(t *testing.T) {
	src := &fakeWatchable{id: "app"}
	plans := []compiled{
		{plan: collect.Plan{ID: "a", SourceID: "app", Mode: config.ModeNotify,
			Watch: &source.WatchSpec{Channel: "one"}}},
		{plan: collect.Plan{ID: "b", SourceID: "app", Mode: config.ModeNotify,
			Watch: &source.WatchSpec{Channel: "two"}}},
	}

	groups := buildWatchGroups(plans, map[string]source.Source{"app": src})
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if groups[0].channel != "one" || groups[1].channel != "two" {
		t.Fatalf("channels = %q, %q, want one, two", groups[0].channel, groups[1].channel)
	}
}

func TestBuildWatchGroupsIgnoresPollingAndUnwatchableSources(t *testing.T) {
	src := &fakeWatchable{id: "app"}
	plans := []compiled{
		{plan: collect.Plan{ID: "poll", SourceID: "app", Mode: config.ModePoll}},
		{plan: collect.Plan{ID: "notify", SourceID: "app", Mode: config.ModeNotify,
			Watch: &source.WatchSpec{Channel: "changes"}}},
	}

	groups := buildWatchGroups(plans, map[string]source.Source{"app": src})
	if len(groups) != 1 || groups[0].channel != "changes" {
		t.Fatalf("groups = %+v, want only the notify plan", groups)
	}

	plain := &fakeSource{id: "plain"}
	groups = buildWatchGroups(plans, map[string]source.Source{"app": plain})
	if len(groups) != 0 {
		t.Fatalf("groups = %+v, want none for a source without Watch", groups)
	}
}

func TestWatchSupervisorForwardsEventsUntilCancelled(t *testing.T) {
	events := make(chan source.ChangeEvent, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &fakeWatchable{id: "app", watch: func(ctx context.Context, _ source.WatchSpec, out chan<- source.ChangeEvent) error {
		// Mirrors the contract: readiness first, then observed changes.
		out <- source.ChangeEvent{SourceID: "app", Kind: source.KindReconnect}
		out <- source.ChangeEvent{SourceID: "app", Kind: source.KindNotify, Scope: "jobs"}
		<-ctx.Done()
		return nil
	}}

	groups := []watchGroup{{sourceID: "app", channel: "changes", src: src, spec: source.WatchSpec{Channel: "changes"}}}
	done := make(chan struct{})
	go func() {
		watchSupervisor(ctx, groups, func(ev source.ChangeEvent) { events <- ev }, slog.New(slog.DiscardHandler))
		close(done)
	}()

	for _, want := range []source.ChangeKind{source.KindReconnect, source.KindNotify} {
		select {
		case ev := <-events:
			if ev.Kind != want {
				t.Fatalf("event kind = %q, want %q", ev.Kind, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("no %q event within 1s", want)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchSupervisor did not return after cancellation")
	}
}

// A fake Source that is not a WatchableSource.
type fakeSource struct{ id string }

func (f *fakeSource) ID() string   { return f.id }
func (f *fakeSource) Type() string { return "fake" }
func (f *fakeSource) Capabilities() source.Capabilities {
	return source.Capabilities{Query: true, Poll: true}
}
func (f *fakeSource) Open(context.Context) error   { return nil }
func (f *fakeSource) Health(context.Context) error { return nil }
func (f *fakeSource) Close() error                 { return nil }
func (f *fakeSource) CompileQuery(source.QueryDefinition) (source.QuerySpec, error) {
	return nil, nil
}
func (f *fakeSource) Query(context.Context, source.QueryRequest) (source.ResultSet, error) {
	return source.ResultSet{}, nil
}

var (
	_ source.Source          = (*fakeWatchable)(nil)
	_ source.WatchableSource = (*fakeWatchable)(nil)
	_ source.Source          = (*fakeSource)(nil)
)
