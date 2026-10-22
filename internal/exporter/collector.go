package exporter

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/snapshot"
)

// Collector exposes prepared snapshots to the Prometheus registry.
//
// Collect reads the snapshot store and nothing else: it runs no query, makes no
// upstream request, and never waits on a collection (P2, §8.1).
type Collector struct {
	store snapshot.Store
	now   func() time.Time
}

// NewCollector wraps a snapshot store as a Prometheus collector.
func NewCollector(store snapshot.Store, now func() time.Time) *Collector {
	if now == nil {
		now = time.Now
	}
	return &Collector{store: store, now: now}
}

// Describe sends nothing, making this an unchecked collector.
//
// Families come from configuration, so the registry cannot know their descriptors at
// registration time. Registering them as unchecked keeps a config reload from
// colliding with a previously described family.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

// Collect emits one metric per published sample.
//
// Staleness is already applied by the store, so a sample reaching here is current by
// the collection's own policy (§8.4).
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	for _, snap := range c.store.Current(c.now()) {
		for _, fam := range snap.Families {
			emitFamily(ch, fam)
		}
	}
}

func emitFamily(ch chan<- prometheus.Metric, fam snapshot.Family) {
	valueType := prometheus.GaugeValue
	if fam.Type == snapshot.TypeCounter {
		valueType = prometheus.CounterValue
	}
	for _, s := range fam.Samples {
		// A per-sample Desc carries the labels as constant labels, which is what lets
		// one family hold series whose label names were only known at mapping time.
		desc := prometheus.NewDesc(fam.Name, fam.Help, nil, constLabels(s.Labels))
		m, err := prometheus.NewConstMetric(desc, valueType, s.Value)
		if err != nil {
			// Surfaces through the registry's error handling instead of being
			// silently dropped (P3).
			ch <- prometheus.NewInvalidMetric(desc, err)
			continue
		}
		ch <- m
	}
}

func constLabels(labels []snapshot.Label) prometheus.Labels {
	if len(labels) == 0 {
		return nil
	}
	out := make(prometheus.Labels, len(labels))
	for _, l := range labels {
		out[l.Name] = l.Value
	}
	return out
}
