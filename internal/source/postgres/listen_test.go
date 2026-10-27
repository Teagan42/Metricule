package postgres

import (
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/source"
)

func TestWatchRequiresAChannel(t *testing.T) {
	s := testSource(t)
	t.Cleanup(func() { _ = s.Close() })

	err := s.Watch(t.Context(), source.WatchSpec{}, make(chan source.ChangeEvent, 1))
	if err == nil {
		t.Fatal("Watch accepted an empty channel, want an error")
	}
	if !strings.Contains(err.Error(), "requires a channel") {
		t.Errorf("error = %v, want a missing-channel diagnostic", err)
	}
}

// A closed source must not start a listener that outlives shutdown.
func TestWatchRefusesAClosedSource(t *testing.T) {
	s := testSource(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := s.Watch(t.Context(), source.WatchSpec{Channel: "changes"}, make(chan source.ChangeEvent, 1))
	if err == nil {
		t.Fatal("Watch started against a closed source")
	}
}

// Only a declared table narrows invalidation. Arbitrary payloads must not filter
// out plans that need the refresh.
func TestScopeForTrustsOnlyDeclaredTables(t *testing.T) {
	scopes := map[string]struct{}{"public.jobs": {}}

	cases := []struct {
		payload string
		want    string
	}{
		{"public.jobs", "public.jobs"},
		{"public.users", source.ScopeUnknown},
		{"", source.ScopeUnknown},
		{"'; DROP TABLE jobs; --", source.ScopeUnknown},
	}
	for _, tc := range cases {
		if got := scopeFor(tc.payload, scopes); got != tc.want {
			t.Errorf("scopeFor(%q) = %q, want %q", tc.payload, got, tc.want)
		}
	}

	if got := scopeFor("public.jobs", nil); got != source.ScopeUnknown {
		t.Errorf("scopeFor with no declared tables = %q, want unknown", got)
	}
}

// The channel is an identifier, not SQL, and configuration does not restrict it.
func TestListenStatementQuotesTheChannel(t *testing.T) {
	got := listenStatement(`weird channel"; DROP TABLE jobs; --`)
	want := `LISTEN "weird channel""; DROP TABLE jobs; --"`
	if got != want {
		t.Errorf("statement = %q, want %q", got, want)
	}
}
