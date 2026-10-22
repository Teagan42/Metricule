package snapshot_test

import (
	"sync"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
)

// Asserts that a scrape only ever observes a complete, self-consistent result while
// publication and failure run concurrently (§8.2, P8).
func TestConcurrentPublishFailAndRead(t *testing.T) {
	t.Parallel()

	const (
		collections = 4
		writers     = 4
		readers     = 8
		iterations  = 500
	)

	store := snapshot.NewMemoryStore(snapshot.StoreConfig{
		Default: snapshot.Policy{Mode: snapshot.Retain, MaxStaleAge: time.Hour},
	})
	base := time.Now()

	// Every publication is one of two complete shapes. A reader that sees any other
	// sample count has observed a partially mapped result.
	shapes := map[int]bool{2: true, 5: true}

	var writeWG, readWG sync.WaitGroup
	stop := make(chan struct{})

	for w := range writers {
		writeWG.Add(1)
		go func(w int) {
			defer writeWG.Done()
			for i := range iterations {
				id := collectionID(i % collections)
				if i%7 == 0 {
					if err := store.Fail(id, base.Add(time.Duration(i)*time.Millisecond)); err != nil {
						t.Errorf("Fail: %v", err)
						return
					}
					continue
				}
				n := 2
				if (i+w)%2 == 0 {
					n = 5
				}
				if err := store.Publish(series(id, base, n)); err != nil {
					t.Errorf("Publish: %v", err)
					return
				}
			}
		}(w)
	}

	for range readers {
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, snap := range store.Current(base.Add(time.Second)) {
					if !shapes[snap.SampleCount()] {
						t.Errorf("scrape observed %d samples for %q, want one of the complete shapes",
							snap.SampleCount(), snap.CollectionID)
						return
					}
				}
			}
		}()
	}

	writeWG.Wait()
	close(stop)
	readWG.Wait()
}

func collectionID(i int) string {
	return string(rune('a' + i))
}

// Builds a snapshot with n distinct series spread over two families.
func series(id string, at time.Time, n int) *snapshot.Snapshot {
	var a, b []snapshot.Sample
	for i := range n {
		s := sample(float64(i+1), "idx", collectionID(i))
		if i%2 == 0 {
			a = append(a, s)
		} else {
			b = append(b, s)
		}
	}
	return &snapshot.Snapshot{
		CollectionID: id,
		SourceID:     "app",
		CollectedAt:  at,
		Families: []snapshot.Family{
			{Name: "app_even", Type: snapshot.TypeGauge, Samples: a},
			{Name: "app_odd", Type: snapshot.TypeGauge, Samples: b},
		},
	}
}
