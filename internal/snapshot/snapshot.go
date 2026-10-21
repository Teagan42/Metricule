// Package snapshot defines the published metric state that scrapes read.
//
// This file declares the contract only. Agent 2 implements Store, whose central
// requirement is that publication is atomic per collection group and that a scrape
// never observes a partially mapped result (§8.2, P8).
package snapshot

import "time"

// Type is the Prometheus wire type of a published family.
//
// A state_set metric lowers to TypeGauge: it is a mapping abstraction, not a wire
// type (§5.1).
type Type string

const (
	TypeGauge   Type = "gauge"
	TypeCounter Type = "counter"
)

// Label is one label pair.
type Label struct {
	Name  string
	Value string
}

// Sample is one published series value.
//
// Labels must be sorted by name before publication so that duplicate labelset
// detection and exposition ordering are both deterministic.
type Sample struct {
	Labels []Label
	Value  float64
}

// Family is one metric family and its current series.
type Family struct {
	Name string
	Help string
	Type Type
	// Samples must contain no duplicate labelsets; duplicates fail the collection
	// rather than overwrite each other (§8.3).
	Samples []Sample
}

// Snapshot is the immutable result of one successful collection group.
//
// Treat as read-only once published. Publication replaces the pointer rather than
// mutating the value, which is what lets scrapes proceed without blocking on
// collection (§8.2).
type Snapshot struct {
	// CollectionID identifies the collection group that produced this snapshot and
	// scopes the series it owns.
	CollectionID string
	SourceID     string
	Families     []Family
	// CollectedAt is when the source data was read, used for staleness decisions.
	CollectedAt time.Time
}

// SampleCount totals published series across families.
func (s *Snapshot) SampleCount() int {
	n := 0
	for _, f := range s.Families {
		n += len(f.Samples)
	}
	return n
}

// Store holds published snapshots for exposition.
//
// Implementations must be safe for concurrent publication and reading. Readers must
// never block on an in-flight collection (P2).
type Store interface {
	// Publish atomically replaces the series owned by snap.CollectionID. Series
	// absent from the new result are removed (§8.3).
	Publish(snap *Snapshot) error

	// Fail records a failed collection at time at, applying the collection's
	// configured staleness policy. Must never substitute zero samples (P3).
	Fail(collectionID string, at time.Time) error

	// Current returns the snapshots visible to a scrape at time now, with samples
	// past their staleness limit already omitted.
	Current(now time.Time) []*Snapshot
}
