package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/teagan42/metricule/internal/collect"
	"github.com/teagan42/metricule/internal/mapping"
	"github.com/teagan42/metricule/internal/source"
)

// collector executes one plan: query the source, map the result, hand back a
// snapshot for publication.
//
// Holds no source-specific logic. It moves a compiled QuerySpec into an adapter and
// a normalized ResultSet into the mapping engine (P7).
type collector struct {
	id       string
	sourceID string
	src      source.Source
	spec     source.QuerySpec
	group    *mapping.Group
	maxRows  int
	now      func() time.Time
}

// ID returns the plan identifier.
func (c *collector) ID() string { return c.id }

// Collect runs the plan once.
//
// Returns an error rather than a partial Result, so the scheduler can never publish
// half a collection. Errors keep the adapter's classification intact so the
// scheduler can label telemetry; wrapping with %w is what preserves it.
func (c *collector) Collect(ctx context.Context) (collect.Result, error) {
	started := c.now()

	rs, err := c.src.Query(ctx, source.QueryRequest{Spec: c.spec, MaxRows: c.maxRows})
	if err != nil {
		return collect.Result{}, fmt.Errorf("collection %q: %w", c.id, err)
	}

	snap, err := c.group.Map(rs, started)
	if err != nil {
		return collect.Result{}, fmt.Errorf("collection %q: %w", c.id, err)
	}

	return collect.Result{
		PlanID:    c.id,
		SourceID:  c.sourceID,
		Snapshot:  snap,
		StartedAt: started,
		Duration:  c.now().Sub(started),
		RowCount:  len(rs.Rows),
	}, nil
}
