package exporter_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/snapshot"
)

// §13.1: /metrics serves valid exposition whenever the process is operational, even
// if one published family is unusable.
func TestMetricsStaysValidWhenOneFamilyIsInvalid(t *testing.T) {
	store := newFakeStore(
		gaugeSnapshot(),
		&snapshot.Snapshot{
			CollectionID: "broken",
			Families: []snapshot.Family{{
				// Configuration rejects such a name; a corrupt snapshot must not take
				// the scrape down with it.
				Name:    "not a valid name",
				Help:    "broken",
				Type:    snapshot.TypeGauge,
				Samples: []snapshot.Sample{{Value: 1}},
			}},
		},
	)

	body, code := get(t, newHandler(t, store, nil), "/metrics")
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if !strings.Contains(body, wantJobsExposition) {
		t.Errorf("healthy family missing:\n%s", body)
	}
	if strings.Contains(body, "not a valid name") {
		t.Errorf("invalid family exposed:\n%s", body)
	}
}
