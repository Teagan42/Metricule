package snapshot

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FailureMode selects what happens to published series after a failed collection.
//
// Mirrors config.FailureMode by value. Duplicated rather than imported because this
// package depends on no other internal package, which is what keeps it free of
// configuration and adapter concerns.
type FailureMode string

const (
	// Retain exposes the last successful snapshot until MaxStaleAge elapses.
	Retain FailureMode = "retain"
	// Drop omits the affected series as soon as a collection fails.
	Drop FailureMode = "drop"
)

// Policy bounds how long a failed collection keeps serving its last good result.
//
// Neither mode ever substitutes a value: expiry omits series (P3, §8.4).
type Policy struct {
	// Mode defaults to Retain when empty.
	Mode FailureMode
	// MaxStaleAge is measured from the snapshot's CollectedAt, so it reports the age
	// of the data rather than the age of the outage. Zero retains nothing.
	MaxStaleAge time.Duration
}

// StoreConfig configures an in-memory store.
type StoreConfig struct {
	// MaxTotalSeries bounds the series published across every collection group. Zero
	// disables the bound. Only the store can enforce it, because no single collection
	// sees the global total (§8.7).
	MaxTotalSeries int

	// Default applies to any collection absent from Policies.
	Default Policy
	// Policies are per-collection overrides, fixed at construction so that a policy
	// cannot change under a concurrent scrape.
	Policies map[string]Policy
}

// MemoryStore holds one immutable snapshot per collection group.
//
// Readers never block on a collection: a scrape takes a read lock on the collection
// index and then loads each published pointer atomically (P2, §8.2).
type MemoryStore struct {
	cfg StoreConfig

	// Serializes writes so that validation, the global series count, and the pointer
	// swap are one atomic step with respect to other writers.
	writeMu sync.Mutex
	total   int

	mu      sync.RWMutex
	entries map[string]*entry
}

// Holds the published state for one collection group.
type entry struct {
	policy Policy
	state  atomic.Pointer[state]
}

// Published state for one collection group. Replaced wholesale, never mutated.
type state struct {
	snap *Snapshot
	// failed marks the last attempt as failed, which is what arms the stale window.
	failed bool
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore builds a store with the given staleness policies.
func NewMemoryStore(cfg StoreConfig) *MemoryStore {
	return &MemoryStore{cfg: cfg, entries: make(map[string]*entry)}
}

// Publish atomically replaces the series owned by snap.CollectionID.
//
// Takes ownership of snap: the caller must not mutate it afterwards (AD-004).
func (s *MemoryStore) Publish(snap *Snapshot) error {
	if err := validate(snap); err != nil {
		return err
	}

	e := s.lookupOrCreate(snap.CollectionID)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Replacement, not addition: the group's own series leave the total first.
	total := s.total - publishedCount(e.state.Load()) + snap.SampleCount()
	if s.cfg.MaxTotalSeries > 0 && total > s.cfg.MaxTotalSeries {
		return fmt.Errorf("collection %q: %d series exceeds the %d total limit: %w",
			snap.CollectionID, total, s.cfg.MaxTotalSeries, ErrTotalSeriesLimit)
	}

	s.total = total
	e.state.Store(&state{snap: snap})
	return nil
}

// Fail records a failed collection at time at and applies the staleness policy.
//
// A collection that has never succeeded has nothing to retain, so this is a no-op.
func (s *MemoryStore) Fail(collectionID string, at time.Time) error {
	s.mu.RLock()
	e := s.entries[collectionID]
	s.mu.RUnlock()
	if e == nil {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	prev := e.state.Load()
	if prev == nil || prev.snap == nil {
		return nil
	}
	if visible(e.policy, prev.snap, at) {
		e.state.Store(&state{snap: prev.snap, failed: true})
		return nil
	}

	// Expired or dropped: release the series rather than holding memory until the
	// next success.
	s.total -= prev.snap.SampleCount()
	e.state.Store(&state{failed: true})
	return nil
}

// Current returns the snapshots visible to a scrape at time now.
//
// Expired retained snapshots are filtered here as well as pruned in Fail, because a
// scrape can land between expiry and the next collection attempt.
func (s *MemoryStore) Current(now time.Time) []*Snapshot {
	entries := s.snapshotIndex()

	out := make([]*Snapshot, 0, len(entries))
	for _, e := range entries {
		st := e.state.Load()
		if st == nil || st.snap == nil {
			continue
		}
		if st.failed && !visible(e.policy, st.snap, now) {
			continue
		}
		out = append(out, st.snap)
	}
	return out
}

// Reports whether a failed collection's last good snapshot may still be exposed.
func visible(p Policy, snap *Snapshot, now time.Time) bool {
	if p.Mode == Drop {
		return false
	}
	return now.Sub(snap.CollectedAt) <= p.MaxStaleAge
}

func publishedCount(st *state) int {
	if st == nil || st.snap == nil {
		return 0
	}
	return st.snap.SampleCount()
}

// Returns the entries ordered by collection ID, holding the index lock only long
// enough to copy pointers.
func (s *MemoryStore) snapshotIndex() []*entry {
	s.mu.RLock()
	ids := make([]string, 0, len(s.entries))
	entries := make([]*entry, 0, len(s.entries))
	for id, e := range s.entries {
		ids = append(ids, id)
		entries = append(entries, e)
	}
	s.mu.RUnlock()

	sort.Sort(byID{ids, entries})
	return entries
}

// Returns the entry for id, creating it on first publication.
func (s *MemoryStore) lookupOrCreate(id string) *entry {
	s.mu.RLock()
	e := s.entries[id]
	s.mu.RUnlock()
	if e != nil {
		return e
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if e = s.entries[id]; e == nil {
		e = &entry{policy: s.policyFor(id)}
		s.entries[id] = e
	}
	return e
}

func (s *MemoryStore) policyFor(id string) Policy {
	p, ok := s.cfg.Policies[id]
	if !ok {
		p = s.cfg.Default
	}
	if p.Mode == "" {
		p.Mode = Retain
	}
	return p
}

// Orders exposition deterministically by collection ID.
type byID struct {
	ids     []string
	entries []*entry
}

func (b byID) Len() int           { return len(b.ids) }
func (b byID) Less(i, j int) bool { return b.ids[i] < b.ids[j] }
func (b byID) Swap(i, j int) {
	b.ids[i], b.ids[j] = b.ids[j], b.ids[i]
	b.entries[i], b.entries[j] = b.entries[j], b.entries[i]
}
