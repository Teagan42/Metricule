package snapshot_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
)

func TestPublishRejectsInvalidSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cases := []struct {
		name string
		snap *snapshot.Snapshot
		want error
	}{
		{
			name: "missing collection id",
			snap: gauge("", now, sample(1)),
			want: snapshot.ErrNoCollectionID,
		},
		{
			name: "duplicate labelset",
			snap: gauge("jobs", now, sample(1, "state", "queued"), sample(2, "state", "queued")),
			want: snapshot.ErrDuplicateSeries,
		},
		{
			name: "duplicate empty labelset",
			snap: gauge("jobs", now, sample(1), sample(2)),
			want: snapshot.ErrDuplicateSeries,
		},
		{
			name: "NaN value",
			snap: gauge("jobs", now, sample(math.NaN())),
			want: snapshot.ErrInvalidSample,
		},
		{
			name: "infinite value",
			snap: gauge("jobs", now, sample(math.Inf(1))),
			want: snapshot.ErrInvalidSample,
		},
		{
			name: "unsorted labels",
			snap: gauge("jobs", now, sample(1, "zone", "a", "state", "queued")),
			want: snapshot.ErrUnsortedLabels,
		},
		{
			name: "duplicate label name",
			snap: gauge("jobs", now, sample(1, "state", "a", "state", "b")),
			want: snapshot.ErrUnsortedLabels,
		},
		{
			name: "duplicate family name",
			snap: &snapshot.Snapshot{
				CollectionID: "jobs",
				CollectedAt:  now,
				Families: []snapshot.Family{
					{Name: "app_jobs", Type: snapshot.TypeGauge},
					{Name: "app_jobs", Type: snapshot.TypeGauge},
				},
			},
			want: snapshot.ErrDuplicateFamily,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
			err := store.Publish(tc.snap)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Publish error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRejectedPublishLeavesPreviousSnapshotIntact(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	now := time.Now()
	good := gauge("jobs", now, sample(7))
	if err := store.Publish(good); err != nil {
		t.Fatalf("Publish good: %v", err)
	}

	if err := store.Publish(gauge("jobs", now.Add(time.Second), sample(math.NaN()))); err == nil {
		t.Fatal("Publish accepted a NaN sample")
	}

	got := store.Current(now)
	if len(got) != 1 || got[0] != good {
		t.Fatalf("a rejected publication replaced the last good snapshot")
	}
}

func TestPublishRejectsTotalSeriesOverflow(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{MaxTotalSeries: 3})
	now := time.Now()

	if err := store.Publish(gauge("a", now, sample(1), sample(2, "k", "v"))); err != nil {
		t.Fatalf("Publish a: %v", err)
	}

	err := store.Publish(gauge("b", now, sample(1), sample(2, "k", "v")))
	if !errors.Is(err, snapshot.ErrTotalSeriesLimit) {
		t.Fatalf("Publish b error = %v, want %v", err, snapshot.ErrTotalSeriesLimit)
	}

	// The overflowing collection must publish nothing, not a truncated result.
	if got := store.Current(now); len(got) != 1 {
		t.Fatalf("Current returned %d snapshots, want 1", len(got))
	}
}

func TestTotalSeriesLimitCountsReplacementNotAddition(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{MaxTotalSeries: 2})
	now := time.Now()

	if err := store.Publish(gauge("a", now, sample(1), sample(2, "k", "v"))); err != nil {
		t.Fatalf("Publish a: %v", err)
	}
	// Replacing a 2-series snapshot with another 2-series snapshot stays at the cap.
	if err := store.Publish(gauge("a", now, sample(3), sample(4, "k", "v"))); err != nil {
		t.Fatalf("republish at the cap: %v", err)
	}
}
