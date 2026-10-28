package exporter_test

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/exporter"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

func TestHistogramExposition(t *testing.T) {
	store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
	if err := store.Publish(&snapshot.Snapshot{
		CollectionID: "c",
		SourceID:     "s",
		CollectedAt:  time.Now(),
		Families: []snapshot.Family{{
			Name: "request_duration_seconds",
			Help: "Request duration",
			Type: snapshot.TypeHistogram,
			Histograms: []snapshot.Histogram{{
				Labels:  []snapshot.Label{{Name: "route", Value: "api"}},
				Buckets: map[float64]uint64{0.1: 1, 1: 3, math.Inf(1): 5},
				Count:   5,
				Sum:     3.5,
			}},
		}},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	registry := prometheus.NewRegistry()
	handler := newHandlerWith(t, exporter.Options{
		Server:    serverConfig(),
		Store:     store,
		Registry:  registry,
		Telemetry: telemetry.New(prometheus.NewRegistry(), version.Get()),
	})
	body, code := get(t, handler, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{
		"# TYPE request_duration_seconds histogram",
		`request_duration_seconds_bucket{route="api",le="0.1"} 1`,
		`request_duration_seconds_bucket{route="api",le="1"} 3`,
		`request_duration_seconds_bucket{route="api",le="+Inf"} 5`,
		`request_duration_seconds_sum{route="api"} 3.5`,
		`request_duration_seconds_count{route="api"} 5`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q:\n%s", want, body)
		}
	}
}
