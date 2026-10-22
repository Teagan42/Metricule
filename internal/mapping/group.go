package mapping

import (
	"fmt"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
)

// GroupSpec is the compile-time input for one collection plan.
type GroupSpec struct {
	CollectionID string
	SourceID     string
	// Metrics are the families the plan publishes. Metrics sharing a query compile
	// into one plan so the query runs once (§7.3).
	Metrics []config.Metric
	// Limits apply to metrics that declare no override.
	Limits config.Limits
}

// Group maps one plan's result set into every family the plan publishes.
//
// Compiled once at startup and safe for concurrent use.
type Group struct {
	collectionID string
	sourceID     string
	mappers      []*Mapper
}

// CompileGroup validates every metric in a plan and returns a reusable group.
func CompileGroup(spec GroupSpec) (*Group, error) {
	g := &Group{collectionID: spec.CollectionID, sourceID: spec.SourceID}
	seen := make(map[string]struct{}, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		if _, dup := seen[metric.Name]; dup {
			return nil, fmt.Errorf("collection %q: duplicate metric family %q",
				spec.CollectionID, metric.Name)
		}
		seen[metric.Name] = struct{}{}

		limits := spec.Limits
		if metric.Limits != nil {
			limits = *metric.Limits
		}
		m, err := Compile(Spec{
			CollectionID: spec.CollectionID,
			SourceID:     spec.SourceID,
			Metric:       metric,
			Limits:       limits,
		})
		if err != nil {
			return nil, fmt.Errorf("collection %q: %w", spec.CollectionID, err)
		}
		g.mappers = append(g.mappers, m)
	}
	return g, nil
}

// ID returns the collection identifier this group publishes under.
func (g *Group) ID() string { return g.collectionID }

// Map converts one result set into a publishable snapshot.
//
// Returns nil on any failure so a caller cannot publish a partially mapped
// collection (§8.2).
func (g *Group) Map(rs source.ResultSet, collectedAt time.Time) (*snapshot.Snapshot, error) {
	snap := &snapshot.Snapshot{
		CollectionID: g.collectionID,
		SourceID:     g.sourceID,
		Families:     make([]snapshot.Family, 0, len(g.mappers)),
		CollectedAt:  collectedAt,
	}
	for _, m := range g.mappers {
		fam, err := m.Map(rs)
		if err != nil {
			return nil, err
		}
		snap.Families = append(snap.Families, fam)
	}
	return snap, nil
}
