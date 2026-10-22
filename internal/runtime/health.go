package runtime

import (
	"context"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// healthInterval paces source reachability checks.
//
// Independent of collection intervals: a source with a slow metric should still
// report reachability promptly, and a source with a fast one should not be probed
// on every refresh.
const healthInterval = 15 * time.Second

// healthProbeTimeout bounds one reachability check (P5).
const healthProbeTimeout = 5 * time.Second

// healthLoop publishes the source-level families until ctx is cancelled.
//
// Owns metricule_source_up and metricule_source_reconnects_total. Adapters receive
// no telemetry handle, and the scheduler holds no source handle, so this is the only
// component positioned to publish them (ADR 0009).
func (r *Runtime) healthLoop(ctx context.Context) error {
	// Reconnect totals are monotonic per source; the counter advances by the delta
	// so a restarted count can never make the exported counter go backwards.
	seen := make(map[string]uint64, len(r.sources))

	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()

	for {
		r.probeAll(ctx, seen)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Runtime) probeAll(ctx context.Context, seen map[string]uint64) {
	for _, name := range sortedSourceNames(r.sources) {
		src := r.sources[name]

		probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
		err := src.Health(probeCtx)
		cancel()

		r.metrics.SetSourceUp(name, err == nil)
		if err != nil {
			r.log.Warn("source health check failed", "source", name, "error", err)
		}

		r.publishReconnects(name, src, seen)
	}
}

// Advances the reconnect counter by however many reconnections happened since the
// last probe.
func (r *Runtime) publishReconnects(name string, src source.Source, seen map[string]uint64) {
	d, ok := src.(source.Diagnosable)
	if !ok {
		return
	}

	total := d.Diagnostics().Reconnects
	previous := seen[name]
	if total <= previous {
		return
	}
	for i := previous; i < total; i++ {
		r.metrics.SourceReconnected(name)
	}
	seen[name] = total
}
