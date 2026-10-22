package exporter_test

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/teagan42/metricule/internal/snapshot"
)

var scrapeTime = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// fakeStore stands in for the real snapshot store.
//
// Current reads an atomic pointer while Publish holds a mutex, mirroring the
// publication design the contract requires, so a handler that serialized on
// publication would show up as a timeout (P2, §8.2).
type fakeStore struct {
	publishing sync.Mutex
	snaps      atomic.Pointer[[]*snapshot.Snapshot]

	currentCalls atomic.Int64
}

func newFakeStore(snaps ...*snapshot.Snapshot) *fakeStore {
	s := &fakeStore{}
	s.snaps.Store(&snaps)
	return s
}

func (s *fakeStore) Publish(snap *snapshot.Snapshot) error {
	s.publishing.Lock()
	defer s.publishing.Unlock()
	next := append(*s.snaps.Load(), snap)
	s.snaps.Store(&next)
	return nil
}

func (s *fakeStore) Fail(string, time.Time) error { return nil }

func (s *fakeStore) Current(time.Time) []*snapshot.Snapshot {
	s.currentCalls.Add(1)
	return *s.snaps.Load()
}

func gaugeSnapshot() *snapshot.Snapshot {
	return &snapshot.Snapshot{
		CollectionID: "appdb-jobs",
		SourceID:     "appdb",
		CollectedAt:  scrapeTime,
		Families: []snapshot.Family{{
			Name: "app_jobs_by_status",
			Help: "Current job count by status",
			Type: snapshot.TypeGauge,
			Samples: []snapshot.Sample{
				{Labels: []snapshot.Label{{Name: "status", Value: "failed"}}, Value: 2},
				{Labels: []snapshot.Label{{Name: "status", Value: "pending"}}, Value: 12},
				{Labels: []snapshot.Label{{Name: "status", Value: "running"}}, Value: 4},
			},
		}},
	}
}
