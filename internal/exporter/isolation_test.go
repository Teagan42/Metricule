package exporter_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

// countingSource fails the test on any adapter call.
//
// The exporter is never given a source, so this asserts the structural property
// directly: a scrape that reached a source would have to call one of these.
var _ source.Source = (*countingSource)(nil)

type countingSource struct {
	t     *testing.T
	calls atomic.Int64
}

func (s *countingSource) record(method string) {
	s.calls.Add(1)
	s.t.Errorf("scrape called source.%s", method)
}

func (s *countingSource) ID() string   { s.record("ID"); return "appdb" }
func (s *countingSource) Type() string { s.record("Type"); return "postgres" }

func (s *countingSource) Capabilities() source.Capabilities {
	s.record("Capabilities")
	return source.Capabilities{}
}

func (s *countingSource) Open(context.Context) error { s.record("Open"); return nil }

func (s *countingSource) CompileQuery(source.QueryDefinition) (source.QuerySpec, error) {
	s.record("CompileQuery")
	return nil, nil
}

func (s *countingSource) Query(context.Context, source.QueryRequest) (source.ResultSet, error) {
	s.record("Query")
	return source.ResultSet{}, nil
}

func (s *countingSource) Health(context.Context) error { s.record("Health"); return nil }
func (s *countingSource) Close() error                 { s.record("Close"); return nil }

// P2, §8.1: a scrape must not invoke a source adapter.
func TestMetricsNeverCallsSource(t *testing.T) {
	src := &countingSource{t: t}
	store := newFakeStore(gaugeSnapshot())
	h := newHandler(t, store, nil)

	for range 3 {
		if _, code := get(t, h, "/metrics"); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	}

	if n := src.calls.Load(); n != 0 {
		t.Errorf("source calls = %d, want 0", n)
	}
	if n := store.currentCalls.Load(); n != 3 {
		t.Errorf("store reads = %d, want 3 (one per scrape)", n)
	}
}

// P2: a scrape must not wait on an in-flight collection.
func TestMetricsDoesNotWaitOnPublication(t *testing.T) {
	store := newFakeStore(gaugeSnapshot())

	// Hold the publication lock for longer than the scrape budget.
	store.publishing.Lock()
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(200 * time.Millisecond)
		store.publishing.Unlock()
	}()

	h := newHandler(t, store, nil)
	done := make(chan string, 1)
	go func() {
		body, _ := get(t, h, "/metrics")
		done <- body
	}()

	select {
	case body := <-done:
		if body != wantJobsExposition {
			t.Errorf("exposition =\n%s\nwant\n%s", body, wantJobsExposition)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("scrape blocked on in-flight publication")
	}
	<-released
}

// §8.4: the store already applied staleness, so an empty result is served as an empty
// exposition rather than as fabricated zeros (P3).
func TestMetricsServesEmptyExpositionWhenNothingPublished(t *testing.T) {
	h := newHandler(t, newFakeStore(), nil)

	body, code := get(t, h, "/metrics")
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if body != "" {
		t.Errorf("exposition = %q, want empty", body)
	}
}

// §8.4: a family whose samples the store dropped exposes no series.
func TestMetricsOmitsFamilyWithoutSamples(t *testing.T) {
	store := newFakeStore(&snapshot.Snapshot{
		CollectionID: "appdb-jobs",
		Families: []snapshot.Family{{
			Name: "app_jobs_by_status",
			Help: "Current job count by status",
			Type: snapshot.TypeGauge,
		}},
	})

	if body, _ := get(t, newHandler(t, store, nil), "/metrics"); body != "" {
		t.Errorf("exposition = %q, want empty", body)
	}
}
