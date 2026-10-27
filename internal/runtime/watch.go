package runtime

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

const (
	// A full channel drops events rather than blocking the watcher. The scheduler
	// coalesces anyway, because one pending invalidation means the same thing as
	// twenty.
	watchBuffer = 16

	// Restart bounds for a watcher that returns. Adapters reconnect internally; this
	// only covers a watcher that cannot establish or keep a subscription.
	watchRestartBase = 250 * time.Millisecond
	watchRestartMax  = 30 * time.Second
	watchStableAfter = time.Minute
)

// watchGroup is one subscription: a source plus the channel it listens on.
//
// Metrics sharing a source and channel share a watcher. Two channels need two
// watchers because WatchSpec carries one channel, and collapsing them would silently
// drop one subscription.
type watchGroup struct {
	sourceID string
	channel  string
	src      source.WatchableSource
	spec     source.WatchSpec
}

// buildWatchGroups folds change-driven plans into subscriptions.
//
// Sources without a Watch implementation are skipped; their plans still reconcile on
// fallback_interval and the compiler already warned about the degraded mode.
func buildWatchGroups(plans []compiled, sources map[string]source.Source) []watchGroup {
	type key struct {
		sourceID string
		channel  string
	}

	groups := make(map[key]*watchGroup)
	order := make([]key, 0, len(plans))

	for _, p := range plans {
		if p.plan.Mode != config.ModeNotify && p.plan.Mode != config.ModeAuto {
			continue
		}
		src, ok := sources[p.plan.SourceID]
		if !ok {
			continue
		}
		watchable, ok := src.(source.WatchableSource)
		if !ok {
			continue
		}

		channel := ""
		if p.plan.Watch != nil {
			channel = p.plan.Watch.Channel
		}
		k := key{sourceID: p.plan.SourceID, channel: channel}
		g, ok := groups[k]
		if !ok {
			g = &watchGroup{
				sourceID: p.plan.SourceID,
				channel:  channel,
				src:      watchable,
				spec:     source.WatchSpec{Channel: channel},
			}
			groups[k] = g
			order = append(order, k)
		}
		if p.plan.Watch != nil {
			g.spec.Tables = mergeUnique(g.spec.Tables, p.plan.Watch.Tables)
			g.spec.Events = mergeUnique(g.spec.Events, p.plan.Watch.Events)
		}
	}

	out := make([]watchGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].sourceID != out[j].sourceID {
			return out[i].sourceID < out[j].sourceID
		}
		return out[i].channel < out[j].channel
	})
	return out
}

// Returns the sorted union of two string slices.
func mergeUnique(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, values := range [][]string{a, b} {
		for _, v := range values {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// watchSupervisor runs one watcher per subscription until ctx is cancelled.
//
// A watcher failure is isolated: it cannot fail the exporter or stop collection.
// The health loop still reports source reachability, and the plan's fallback
// interval still repairs anything missed while the subscription is down.
func watchSupervisor(ctx context.Context, groups []watchGroup, invalidate func(source.ChangeEvent), log *slog.Logger) {
	var wg sync.WaitGroup
	for _, g := range groups {
		wg.Add(1)
		go func(g watchGroup) {
			defer wg.Done()
			runWatchGroup(ctx, g, invalidate, log)
		}(g)
	}
	wg.Wait()
}

func runWatchGroup(ctx context.Context, g watchGroup, invalidate func(source.ChangeEvent), log *slog.Logger) {
	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan source.ChangeEvent, watchBuffer)
	var forward sync.WaitGroup
	forward.Add(1)
	go func() {
		defer forward.Done()
		for {
			select {
			case <-groupCtx.Done():
				return
			case ev := <-events:
				invalidate(ev)
			}
		}
	}()
	defer forward.Wait()

	delay := watchRestartBase
	for {
		started := time.Now()
		err := g.src.Watch(groupCtx, g.spec, events)
		if groupCtx.Err() != nil {
			return
		}

		attrs := []any{"source", g.sourceID, "channel", g.channel}
		if err != nil {
			log.Warn("change watcher stopped", append(attrs, "error", err)...)
		} else {
			log.Warn("change watcher exited without an error", attrs...)
		}

		// A long-lived subscription that eventually returns is a fresh start, not a
		// crash loop; do not keep the previous backoff.
		if time.Since(started) >= watchStableAfter {
			delay = watchRestartBase
		}

		timer := time.NewTimer(delay)
		select {
		case <-groupCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if delay < watchRestartMax {
			delay = min(delay*2, watchRestartMax)
		}
	}
}
