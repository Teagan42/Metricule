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
	seen := make(map[string]uint64)
	var current *generation
	var known map[string]struct{}

	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()

	for {
		gen := r.gen.Load()
		if gen != nil {
			if gen != current {
				current = gen
				// A new generation starts reconnect totals from zero and may drop
				// sources entirely. Drop their reachability series so a removed
				// source does not look permanently up.
				nextSeen := make(map[string]uint64, len(gen.sources))
				next := make(map[string]struct{}, len(gen.sources))
				for name := range gen.sources {
					next[name] = struct{}{}
					// Preserve the reconnect high-water mark for a source that
					// survives the reload, so its counter is not republished.
					if v, ok := seen[name]; ok {
						nextSeen[name] = v
					}
				}
				seen = nextSeen
				for name := range known {
					if _, ok := next[name]; !ok {
						r.metrics.DeleteSourceUp(name)
					}
				}
				known = next
			}
			r.probeAll(ctx, gen, seen)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Runtime) probeAll(ctx context.Context, gen *generation, seen map[string]uint64) {
	for _, name := range sortedSourceNames(gen.sources) {
		src := gen.sources[name]

		probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
		err := src.Health(probeCtx)
		cancel()

		// A reload may have replaced this generation while the probe was running.
		// Do not overwrite the new generation's reachability with an old result.
		if r.gen.Load() != gen {
			return
		}
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
