package snapshot

import (
	"fmt"
	"runtime"
	"strconv"
	"testing"
	"time"
)

var benchSizes = []int{1_000, 10_000, 100_000}

func benchmarkSnapshot(series int) *Snapshot {
	samples := make([]Sample, series)
	for i := range samples {
		samples[i] = Sample{
			Labels: []Label{{Name: "id", Value: strconv.Itoa(i)}},
			Value:  float64(i),
		}
	}
	return &Snapshot{
		CollectionID: "bench",
		SourceID:     "bench",
		Families: []Family{{
			Name:    "bench_series",
			Help:    "Benchmark series",
			Type:    TypeGauge,
			Samples: samples,
		}},
		CollectedAt: time.Now(),
	}
}

// Publication cost and allocations for a full replacement at each series count.
func BenchmarkPublish(b *testing.B) {
	for _, series := range benchSizes {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			store := NewMemoryStore(StoreConfig{})
			snap := benchmarkSnapshot(series)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := store.Publish(snap); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(series), "series/op")
		})
	}
}

// Scrape read cost: taking the current snapshot must not allocate per sample.
func BenchmarkCurrent(b *testing.B) {
	for _, series := range benchSizes {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			store := NewMemoryStore(StoreConfig{})
			snap := benchmarkSnapshot(series)
			if err := store.Publish(snap); err != nil {
				b.Fatal(err)
			}
			now := time.Now()

			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for range b.N {
				for _, current := range store.Current(now) {
					sink += current.SampleCount()
				}
			}
			if sink == 0 {
				b.Fatal("no series read")
			}
		})
	}
}

// Retained heap per published series.
//
// Reports one busy loop so the measurement is a snapshot of heap after publication,
// not a cumulative allocation counter. GC is forced before and after so the delta
// reflects live series rather than garbage.
func BenchmarkMemoryPerPublishedSeries(b *testing.B) {
	for _, series := range benchSizes {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			for range b.N {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)

				store := NewMemoryStore(StoreConfig{})
				if err := store.Publish(benchmarkSnapshot(series)); err != nil {
					b.Fatal(err)
				}

				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(store)

				delta := float64(after.HeapAlloc) - float64(before.HeapAlloc)
				b.ReportMetric(delta/float64(series), "bytes/series")
			}
		})
	}
}
