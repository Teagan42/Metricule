package collect_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

// Exercises scrapes, refreshes, notifications, failures, and shutdown at the same
// time. Run under -race this is the gate for P8 and §20.5.
func TestConcurrentScrapeRefreshInvalidateAndShutdown(t *testing.T) {
	t.Parallel()

	opts, store := testOptions(t)
	opts.MaxRefreshDelay = 5 * time.Millisecond
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	const plans = 6
	colls := make([]*fakeCollector, plans)
	for i := range colls {
		id := "plan" + string(rune('a'+i))
		colls[i] = newFakeCollector(id, func(ctx context.Context, call int) (collect.Result, error) {
			// Every third attempt fails, so the staleness policy and retry path run
			// alongside successful publication.
			if call%3 == 0 {
				return collect.Result{}, errSourceDown
			}
			select {
			case <-ctx.Done():
				return collect.Result{}, ctx.Err()
			case <-time.After(time.Millisecond):
			}
			return wideResult(id, call), nil
		})

		plan := testPlan(id, 2*time.Millisecond)
		plan.SourceID = "src" + string(rune('a'+i%2))
		plan.Debounce = time.Millisecond
		plan.Reconcile = 7 * time.Millisecond
		plan.Limits = config.Limits{MaxConcurrentCollections: 2}
		if i%2 == 0 {
			plan.Failure = config.Failure{Mode: config.FailureDrop}
		}
		if err := sched.Add(plan, colls[i]); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Scrapers.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, snap := range store.Current(time.Now()) {
					// 3 and 9 are the only complete shapes wideResult produces.
					if n := snap.SampleCount(); n != 3 && n != 9 {
						t.Errorf("scrape saw %d samples for %q, want a complete shape",
							n, snap.CollectionID)
						return
					}
				}
			}
		}()
	}

	// Notification senders, including unknown-scope events.
	for i := range 3 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sched.Invalidate(source.ChangeEvent{
					SourceID: "src" + string(rune('a'+i%2)),
					Kind:     source.KindNotify,
					At:       time.Now(),
				})
			}
		}(i)
	}

	// Readiness pollers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = sched.Status().Ready()
		}
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not unwind within 5s")
	}

	close(stop)
	wg.Wait()

	for _, c := range colls {
		if c.peakConcurrency() > 1 {
			t.Errorf("plan %q collected %d times concurrently with itself",
				c.ID(), c.peakConcurrency())
		}
		if c.count() == 0 {
			t.Errorf("plan %q never collected", c.ID())
		}
	}
}

// Builds a snapshot whose sample count alternates between two complete shapes.
func wideResult(id string, call int) collect.Result {
	n := 3
	if call%2 == 0 {
		n = 9
	}
	families := []snapshot.Family{
		{Name: "app_" + id + "_a", Type: snapshot.TypeGauge},
		{Name: "app_" + id + "_b", Type: snapshot.TypeGauge},
	}
	for i := range n {
		s := snapshot.Sample{
			Labels: []snapshot.Label{{Name: "idx", Value: string(rune('a' + i))}},
			Value:  float64(i + 1),
		}
		families[i%2].Samples = append(families[i%2].Samples, s)
	}
	return collect.Result{
		PlanID:   id,
		SourceID: "app",
		RowCount: n,
		Snapshot: &snapshot.Snapshot{
			CollectionID: id,
			SourceID:     "app",
			CollectedAt:  time.Now(),
			Families:     families,
		},
	}
}
