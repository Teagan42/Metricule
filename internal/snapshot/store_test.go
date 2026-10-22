package snapshot_test

import (
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
)

func TestPublishedSnapshotIsVisibleToCurrent(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	now := time.Now()
	snap := &snapshot.Snapshot{
		CollectionID: "jobs",
		SourceID:     "app",
		CollectedAt:  now,
		Families: []snapshot.Family{{
			Name:    "app_jobs",
			Help:    "Jobs.",
			Type:    snapshot.TypeGauge,
			Samples: []snapshot.Sample{{Value: 3}},
		}},
	}

	if err := store.Publish(snap); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := store.Current(now)
	if len(got) != 1 {
		t.Fatalf("Current returned %d snapshots, want 1", len(got))
	}
	if got[0] != snap {
		t.Fatalf("Current returned a different snapshot than was published")
	}
}

func TestRepublishRemovesSeriesAbsentFromNewResult(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	now := time.Now()

	first := gauge("jobs", now, sample(1, "state", "queued"), sample(2, "state", "running"))
	if err := store.Publish(first); err != nil {
		t.Fatalf("Publish first: %v", err)
	}

	second := gauge("jobs", now.Add(time.Second), sample(5, "state", "queued"))
	if err := store.Publish(second); err != nil {
		t.Fatalf("Publish second: %v", err)
	}

	got := store.Current(now.Add(time.Second))
	if len(got) != 1 {
		t.Fatalf("Current returned %d snapshots, want 1", len(got))
	}
	if n := got[0].SampleCount(); n != 1 {
		t.Fatalf("SampleCount = %d, want 1: the running series must be removed", n)
	}
	if v := got[0].Families[0].Samples[0].Value; v != 5 {
		t.Fatalf("queued value = %v, want 5", v)
	}
}

func TestCurrentOrdersCollectionsDeterministically(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	now := time.Now()
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if err := store.Publish(gauge(id, now, sample(1))); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}

	got := store.Current(now)
	want := []string{"alpha", "mid", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("Current returned %d snapshots, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].CollectionID != id {
			t.Fatalf("Current()[%d].CollectionID = %q, want %q", i, got[i].CollectionID, id)
		}
	}
}

// Builds a single-family gauge snapshot for one collection group.
func gauge(id string, at time.Time, samples ...snapshot.Sample) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		CollectionID: id,
		SourceID:     "app",
		CollectedAt:  at,
		Families: []snapshot.Family{{
			Name:    "app_" + id,
			Help:    "Test family.",
			Type:    snapshot.TypeGauge,
			Samples: samples,
		}},
	}
}

// Builds a sample from alternating label name/value pairs.
func sample(value float64, labels ...string) snapshot.Sample {
	s := snapshot.Sample{Value: value}
	for i := 0; i+1 < len(labels); i += 2 {
		s.Labels = append(s.Labels, snapshot.Label{Name: labels[i], Value: labels[i+1]})
	}
	return s
}
