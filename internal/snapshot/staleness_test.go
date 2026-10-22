package snapshot_test

import (
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
)

func TestFailRetainsLastSnapshotWithinMaxStaleAge(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: 5 * time.Minute},
	})
	at := time.Now()
	good := gauge("jobs", at, sample(7))
	if err := store.Publish(good); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := store.Fail("jobs", at.Add(30*time.Second)); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	got := store.Current(at.Add(4 * time.Minute))
	if len(got) != 1 || got[0] != good {
		t.Fatalf("retain mode dropped a snapshot that is still inside its stale window")
	}
}

func TestFailOmitsSnapshotAfterMaxStaleAge(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: time.Minute},
	})
	at := time.Now()
	if err := store.Publish(gauge("jobs", at, sample(7))); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := store.Fail("jobs", at.Add(30*time.Second)); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if got := store.Current(at.Add(90 * time.Second)); len(got) != 0 {
		t.Fatalf("Current returned %d snapshots past max_stale_age, want 0", len(got))
	}
}

func TestFailDropsImmediatelyUnderDropMode(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Drop},
	})
	at := time.Now()
	if err := store.Publish(gauge("jobs", at, sample(7))); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := store.Fail("jobs", at); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	got := store.Current(at)
	if len(got) != 0 {
		t.Fatalf("drop mode exposed %d snapshots after failure, want 0: %+v", len(got), got[0])
	}
}

func TestFailNeverSubstitutesZeroSamples(t *testing.T) {
	t.Parallel()

	for _, mode := range []snapshot.FailureMode{snapshot.Retain, snapshot.Drop} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			store := snapshot.NewMemoryStore(snapshot.StoreConfig{
				Default: snapshot.Policy{Mode: mode, MaxStaleAge: time.Minute},
			})
			at := time.Now()
			if err := store.Publish(gauge("jobs", at, sample(7))); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if err := store.Fail("jobs", at.Add(2*time.Minute)); err != nil {
				t.Fatalf("Fail: %v", err)
			}

			for _, snap := range store.Current(at.Add(2 * time.Minute)) {
				for _, f := range snap.Families {
					for _, s := range f.Samples {
						if s.Value == 0 {
							t.Fatalf("failure produced a zero sample in %s", f.Name)
						}
					}
				}
			}
		})
	}
}

func TestPerCollectionPolicyOverridesDefault(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default:  snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: time.Hour},
		Policies: map[string]snapshot.Policy{"strict": {Mode: snapshot.Drop}},
	})
	at := time.Now()
	for _, id := range []string{"lenient", "strict"} {
		if err := store.Publish(gauge(id, at, sample(1))); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
		if err := store.Fail(id, at); err != nil {
			t.Fatalf("Fail %s: %v", id, err)
		}
	}

	got := store.Current(at)
	if len(got) != 1 || got[0].CollectionID != "lenient" {
		t.Fatalf("Current = %v, want only the lenient collection", ids(got))
	}
}

func TestSuccessAfterFailureRestoresVisibilityAndClearsStaleness(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Drop},
	})
	at := time.Now()
	if err := store.Publish(gauge("jobs", at, sample(1))); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := store.Fail("jobs", at); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	fresh := gauge("jobs", at.Add(time.Minute), sample(2))
	if err := store.Publish(fresh); err != nil {
		t.Fatalf("Publish fresh: %v", err)
	}

	got := store.Current(at.Add(time.Minute))
	if len(got) != 1 || got[0] != fresh {
		t.Fatalf("a successful collection did not clear the failed state")
	}
}

func TestFailOnUnknownCollectionIsNoOp(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	if err := store.Fail("never-collected", time.Now()); err != nil {
		t.Fatalf("Fail on an unknown collection: %v", err)
	}
	if got := store.Current(time.Now()); len(got) != 0 {
		t.Fatalf("Current returned %d snapshots, want 0", len(got))
	}
}

func TestDroppedSeriesReleaseGlobalCapacity(t *testing.T) {
	t.Parallel()

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		MaxTotalSeries: 2,
		Default:        snapshot.Policy{Mode: snapshot.Drop},
	})
	at := time.Now()
	if err := store.Publish(gauge("a", at, sample(1), sample(2, "k", "v"))); err != nil {
		t.Fatalf("Publish a: %v", err)
	}
	if err := store.Fail("a", at); err != nil {
		t.Fatalf("Fail a: %v", err)
	}

	if err := store.Publish(gauge("b", at, sample(1), sample(2, "k", "v"))); err != nil {
		t.Fatalf("Publish b after a was dropped: %v", err)
	}
}

func ids(snaps []*snapshot.Snapshot) []string {
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.CollectionID
	}
	return out
}
