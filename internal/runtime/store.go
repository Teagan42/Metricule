package runtime

import (
	"sync/atomic"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
)

// storeSlot is the store the exporter reads.
//
// A reload replaces the whole store: removed metrics must disappear, and a new
// generation must not inherit stale snapshots. The slot makes that replacement
// atomic, so a concurrent scrape sees either the old store or the new one.
type storeSlot struct {
	store atomic.Pointer[snapshot.MemoryStore]
}

func newStoreSlot(store *snapshot.MemoryStore) *storeSlot {
	s := &storeSlot{}
	s.store.Store(store)
	return s
}

func (s *storeSlot) current() *snapshot.MemoryStore { return s.store.Load() }

func (s *storeSlot) swap(store *snapshot.MemoryStore) { s.store.Store(store) }

// Publish writes to the store installed for the current generation.
func (s *storeSlot) Publish(snap *snapshot.Snapshot) error {
	return s.current().Publish(snap)
}

// Fail records a failed collection in the current generation's store.
func (s *storeSlot) Fail(collectionID string, at time.Time) error {
	return s.current().Fail(collectionID, at)
}

// Current reads the current generation's published snapshots.
func (s *storeSlot) Current(now time.Time) []*snapshot.Snapshot {
	return s.current().Current(now)
}
