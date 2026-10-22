package snapshot

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// StoreConfig configures an in-memory store.
type StoreConfig struct{}

// MemoryStore holds one immutable snapshot per collection group.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]*entry
}

// Holds the published state for one collection group.
type entry struct {
	state atomic.Pointer[Snapshot]
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore builds a store with the given staleness policies.
func NewMemoryStore(_ StoreConfig) *MemoryStore {
	return &MemoryStore{entries: make(map[string]*entry)}
}

// Publish atomically replaces the series owned by snap.CollectionID.
func (s *MemoryStore) Publish(snap *Snapshot) error {
	s.lookupOrCreate(snap.CollectionID).state.Store(snap)
	return nil
}

// Fail records a failed collection at time at.
func (s *MemoryStore) Fail(_ string, _ time.Time) error { return nil }

// Current returns the snapshots visible to a scrape at time now.
func (s *MemoryStore) Current(_ time.Time) []*Snapshot {
	s.mu.RLock()
	ids := make([]string, 0, len(s.entries))
	entries := make([]*entry, 0, len(s.entries))
	for id, e := range s.entries {
		ids = append(ids, id)
		entries = append(entries, e)
	}
	s.mu.RUnlock()

	sort.Sort(byID{ids, entries})

	out := make([]*Snapshot, 0, len(entries))
	for _, e := range entries {
		if snap := e.state.Load(); snap != nil {
			out = append(out, snap)
		}
	}
	return out
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
		e = &entry{}
		s.entries[id] = e
	}
	return e
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
