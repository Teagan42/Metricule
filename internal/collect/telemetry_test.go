package collect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
)

func TestSuccessfulCollectionRecordsTelemetry(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	coll := succeedingCollector("jobs")
	if err := sched.Add(testPlan("jobs", time.Hour), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return familyTotal(t, reg, "metricule_collection_success") == 1
	})
	stop()

	for _, name := range []string{
		"metricule_collection_refresh_total",
		"metricule_collection_samples",
		"metricule_collection_last_success_timestamp_seconds",
	} {
		if familyTotal(t, reg, name) == 0 {
			t.Errorf("%s was never recorded", name)
		}
	}
}

func TestFailedCollectionRecordsErrorsAndClearsSuccess(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		return collect.Result{}, errSourceDown
	})
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return familyTotal(t, reg, "metricule_collection_errors_total") > 0
	})
	stop()

	if got := familyTotal(t, reg, "metricule_collection_success"); got != 0 {
		t.Fatalf("collection_success = %v, want 0 after a failure", got)
	}
	if got := familyTotal(t, reg, "metricule_collection_samples"); got != 0 {
		t.Fatalf("collection_samples = %v, want 0: a failure must not imply a sample count", got)
	}
}

func TestCardinalityOverflowIsReportedAsARejection(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	opts.Store = snapshot.NewMemoryStore(snapshot.StoreConfig{
		MaxTotalSeries: 1,
		Default:        snapshot.Policy{Mode: snapshot.Drop},
	})
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		res := okResult("jobs", 1)
		res.Snapshot.Families[0].Samples = []snapshot.Sample{
			{Labels: []snapshot.Label{{Name: "k", Value: "a"}}, Value: 1},
			{Labels: []snapshot.Label{{Name: "k", Value: "b"}}, Value: 2},
		}
		return res, nil
	})
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return familyTotal(t, reg, "metricule_cardinality_rejections_total") > 0
	})
	stop()
}

func TestMalformedSnapshotIsReportedAsAMappingError(t *testing.T) {
	t.Parallel()

	opts, _, reg := testOptionsWithRegistry(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	coll := newFakeCollector("jobs", func(_ context.Context, _ int) (collect.Result, error) {
		res := okResult("jobs", 1)
		res.Snapshot.Families[0].Samples = []snapshot.Sample{{Value: 1}, {Value: 2}}
		return res, nil
	})
	if err := sched.Add(testPlan("jobs", 0), coll); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	coll.waitCalls(t, 1, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		return familyTotal(t, reg, "metricule_mapping_errors_total") > 0
	})
	stop()
}

func TestUnsupportedCollectModesAreRejected(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	for _, mode := range []config.CollectMode{config.ModeManual, config.ModeCDC} {
		plan := testPlan("jobs-"+string(mode), time.Hour)
		plan.Mode = mode
		if err := sched.Add(plan, succeedingCollector(plan.ID)); !errors.Is(err, collect.ErrUnsupportedMode) {
			t.Fatalf("Add(%s) error = %v, want ErrUnsupportedMode", mode, err)
		}
	}
}

func TestRunRejectsASecondCall(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)
	sched, err := collect.NewScheduler(opts)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := sched.Add(testPlan("jobs", time.Hour), succeedingCollector("jobs")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := startScheduler(t, sched)
	waitFor(t, 2*time.Second, func() bool { return sched.Status().Attempted == 1 })

	if err := sched.Run(context.Background()); !errors.Is(err, collect.ErrAlreadyStarted) {
		t.Fatalf("second Run = %v, want ErrAlreadyStarted", err)
	}
	if err := sched.Add(testPlan("late", time.Hour), succeedingCollector("late")); !errors.Is(err, collect.ErrAlreadyStarted) {
		t.Fatalf("Add after Run = %v, want ErrAlreadyStarted", err)
	}
	stop()
}

func TestNewSchedulerRequiresStoreAndMetrics(t *testing.T) {
	t.Parallel()

	opts, _ := testOptions(t)

	noStore := opts
	noStore.Store = nil
	if _, err := collect.NewScheduler(noStore); err == nil {
		t.Fatal("NewScheduler accepted a nil store")
	}

	noMetrics := opts
	noMetrics.Metrics = nil
	if _, err := collect.NewScheduler(noMetrics); err == nil {
		t.Fatal("NewScheduler accepted nil metrics")
	}
}
