package exporter_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/teagan42/metricule/internal/snapshot"
)

// P8: concurrent scrapes and publications never expose a partial result or race.
func TestConcurrentScrapesAndPublications(t *testing.T) {
	store := newFakeStore(gaugeSnapshot())
	h := newHandler(t, store, nil)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			store.Publish(&snapshot.Snapshot{
				CollectionID: "extra",
				Families: []snapshot.Family{{
					Name: "app_extra", Help: "extra", Type: snapshot.TypeGauge,
					Samples: []snapshot.Sample{{
						Labels: []snapshot.Label{{Name: "n", Value: string(rune('a' + i))}},
						Value:  float64(i),
					}},
				}},
			})
		})
		wg.Go(func() {
			body, code := get(t, h, "/metrics")
			if code != http.StatusOK {
				t.Errorf("status = %d, want 200", code)
			}
			// Any observed response must contain whole series, never a fragment.
			if countLines(body, "app_jobs_by_status{") != 3 {
				t.Errorf("partial family observed:\n%s", body)
			}
		})
	}
	wg.Wait()
}
