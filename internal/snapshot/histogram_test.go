package snapshot

import (
	"math"
	"strings"
	"testing"
	"time"
)

func histogramSnapshot() *Snapshot {
	return &Snapshot{
		CollectionID: "c",
		SourceID:     "s",
		CollectedAt:  time.Now(),
		Families: []Family{{
			Name: "request_duration_seconds",
			Help: "Request duration",
			Type: TypeHistogram,
			Histograms: []Histogram{{
				Labels:  []Label{{Name: "route", Value: "api"}},
				Buckets: map[float64]uint64{0.1: 1, 1: 3, math.Inf(1): 5},
				Count:   5,
				Sum:     3.5,
			}},
		}},
	}
}

func TestHistogramPublishesAndCountsGeneratedSeries(t *testing.T) {
	snap := histogramSnapshot()
	if got := snap.SampleCount(); got != 5 {
		t.Errorf("SampleCount = %d, want three buckets plus _sum and _count", got)
	}
	store := NewMemoryStore(StoreConfig{MaxTotalSeries: 5})
	if err := store.Publish(snap); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := len(store.Current(time.Now())); got != 1 {
		t.Errorf("current = %d snapshots", got)
	}
}

func TestHistogramValidation(t *testing.T) {
	cases := map[string]func(*Histogram){
		"missing +Inf":     func(h *Histogram) { delete(h.Buckets, math.Inf(1)) },
		"count mismatch":   func(h *Histogram) { h.Count = 4 },
		"non-cumulative":   func(h *Histogram) { h.Buckets[1] = 0 },
		"nan sum":          func(h *Histogram) { h.Sum = math.NaN() },
		"no buckets":       func(h *Histogram) { h.Buckets = nil },
		"duplicate labels": func(h *Histogram) { h.Labels = append(h.Labels, h.Labels[0]) },
	}
	for name, mutate := range cases {
		snap := histogramSnapshot()
		mutate(&snap.Families[0].Histograms[0])
		if err := validate(snap); err == nil {
			t.Errorf("%s: snapshot accepted", name)
		}
	}
}

func TestHistogramFamilyRejectsFlatSamples(t *testing.T) {
	snap := histogramSnapshot()
	snap.Families[0].Samples = []Sample{{Value: 1}}
	if err := validate(snap); err == nil || !strings.Contains(err.Error(), "samples present") {
		t.Fatalf("error = %v, want a flat-sample rejection", err)
	}
}

func TestFlatFamilyRejectsHistograms(t *testing.T) {
	snap := &Snapshot{
		CollectionID: "c",
		Families: []Family{{
			Name: "g", Help: "g", Type: TypeGauge,
			Histograms: []Histogram{{Buckets: map[float64]uint64{math.Inf(1): 0}}},
		}},
	}
	if err := validate(snap); err == nil {
		t.Fatal("gauge family with histograms accepted")
	}
}
