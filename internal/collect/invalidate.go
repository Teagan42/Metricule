package collect

import (
	"time"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// DefaultMaxRefreshFactor caps a trailing debounce at ten debounce windows.
//
// Without a cap, a stream of writes that keeps resetting the window would postpone
// the refresh forever (§7.5).
const DefaultMaxRefreshFactor = 10

// Invalidate marks the plans affected by a change event for refresh.
//
// Debouncing is trailing-edge with a maximum delay: the refresh runs once the event
// stream has been quiet for the plan's debounce window, or at the maximum delay
// after the first event of a burst, whichever comes first. There is no leading-edge
// refresh, because querying on the first event of a burst reads data that the rest
// of the burst immediately invalidates, doubling query load for a result that is
// stale on arrival.
//
// Never blocks and never queues. Each plan holds one pending-invalidation slot; an
// event that finds it full is folded into the pending refresh and counted, because a
// change signal carries no information beyond "something may have changed" (AD-005,
// rule 10).
func (s *Scheduler) Invalidate(ev source.ChangeEvent) {
	s.mu.Lock()
	runners := s.bySource[ev.SourceID]
	s.mu.Unlock()

	for _, r := range runners {
		if !r.watches(ev.Scope) {
			continue
		}
		s.opts.Metrics.TriggerEventReceived(ev.SourceID, r.plan.ID)
		select {
		case r.events <- ev:
		default:
			s.opts.Metrics.EventsCoalesced(r.plan.ID, 1)
		}
	}
}

// Reports whether a change in scope can affect this plan.
//
// An unknown scope invalidates every plan on the source, and a plan that declares no
// tables is assumed to depend on the whole source (§7.4).
func (r *runner) watches(scope string) bool {
	if scope == source.ScopeUnknown {
		return true
	}
	if r.plan.Watch == nil || len(r.plan.Watch.Tables) == 0 {
		return true
	}
	for _, t := range r.plan.Watch.Tables {
		if t == scope {
			return true
		}
	}
	return false
}

// Bounds how long a trailing debounce may postpone a refresh.
func (r *runner) maxRefreshDelay() time.Duration {
	if d := r.sched.opts.MaxRefreshDelay; d > 0 {
		return d
	}
	return DefaultMaxRefreshFactor * r.plan.Debounce
}

// Maps a change kind onto the refresh trigger recorded in telemetry.
func triggerFor(kind source.ChangeKind) telemetry.Trigger {
	switch kind {
	case source.KindToken:
		return telemetry.TriggerToken
	case source.KindReconnect:
		return telemetry.TriggerReconnect
	case source.KindReconcile:
		return telemetry.TriggerReconcile
	case source.KindNotify:
		return telemetry.TriggerNotify
	default:
		return telemetry.TriggerNotify
	}
}
