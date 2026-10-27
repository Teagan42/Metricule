package exporter

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
	"github.com/teagan42/metricule/internal/version"
)

var scrapeSizes = []int{1_000, 10_000, 100_000}

func scrapeSnapshot(series int) *snapshot.Snapshot {
	samples := make([]snapshot.Sample, series)
	for i := range samples {
		samples[i] = snapshot.Sample{
			Labels: []snapshot.Label{{Name: "id", Value: strconv.Itoa(i)}},
			Value:  float64(i),
		}
	}
	return &snapshot.Snapshot{
		CollectionID: "bench",
		SourceID:     "bench",
		Families: []snapshot.Family{{
			Name:    "bench_series",
			Help:    "Benchmark series",
			Type:    snapshot.TypeGauge,
			Samples: samples,
		}},
		CollectedAt: time.Now(),
	}
}

// Scrape latency across published series counts (P2, §20.6).
//
// The handler is exercised directly rather than over a socket: the measurement is
// snapshot read plus text encoding, not TCP overhead.
func BenchmarkScrape(b *testing.B) {
	for _, series := range scrapeSizes {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			store := snapshot.NewMemoryStore(snapshot.StoreConfig{})
			if err := store.Publish(scrapeSnapshot(series)); err != nil {
				b.Fatal(err)
			}

			registry := prometheus.NewRegistry()
			srv, err := New(Options{
				Server: config.Server{
					MetricsPath:   "/metrics",
					HealthPath:    "/healthz",
					ReadinessPath: "/readyz",
				},
				Store:     store,
				Registry:  registry,
				Telemetry: telemetry.New(registry, version.Get()),
				Logger:    slog.New(slog.DiscardHandler),
			})
			if err != nil {
				b.Fatal(err)
			}
			handler := srv.Handler()

			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			b.ReportAllocs()
			b.ResetTimer()

			bytesOut := 0
			for range b.N {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatalf("status = %d", rec.Code)
				}
				bytesOut += rec.Body.Len()
			}
			b.ReportMetric(float64(series), "series/op")
			b.ReportMetric(float64(bytesOut)/float64(b.N), "body-bytes/op")
		})
	}
}
