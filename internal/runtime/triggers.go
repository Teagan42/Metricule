package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/triggers"
)

// Bounds one provisioning operation. Trigger DDL is short; a lock wait beyond
// this is an operator problem, not something to block startup for (P5).
const triggerOperationTimeout = 30 * time.Second

// BuildTriggerTargets resolves every source that configured a triggers block.
//
// A block is present even when manage is false, because status and remove must
// work without re-enabling provisioning. Resolving here keeps secrets out of the
// triggers package (§14.3).
func BuildTriggerTargets(cfg *config.Config) ([]triggers.Target, error) {
	var out []triggers.Target

	for _, name := range sortedNames(cfg.Sources) {
		p := cfg.Sources[name].Postgres
		if p == nil || p.Triggers == nil {
			continue
		}

		dsn, err := p.DSN.Resolve()
		if err != nil {
			return nil, fmt.Errorf("source %q: dsn: %w", name, err)
		}
		if !p.Triggers.DSN.IsZero() {
			// Provisioning normally needs privileges the collection role lacks.
			if dsn, err = p.Triggers.DSN.Resolve(); err != nil {
				return nil, fmt.Errorf("source %q: triggers.dsn: %w", name, err)
			}
		}

		subs, warnings := triggerSubscriptions(cfg, name)
		out = append(out, triggers.Target{
			SourceID:          name,
			Schema:            p.Triggers.Schema,
			Manage:            p.Triggers.Manage,
			CleanupOnShutdown: p.Triggers.CleanupOnShutdown,
			DSN:               dsn,
			Subscriptions:     subs,
			Warnings:          warnings,
		})
	}
	return out, nil
}

// Derives table/channel subscriptions from change-driven metrics.
//
// Managed triggers are table-level, not metric-level: two metrics sharing a table
// and channel produce one trigger with the union of their events (§9.3). An empty
// event list means all events, so any metric that omits the list makes the union
// all events.
func triggerSubscriptions(cfg *config.Config, source string) ([]triggers.Subscription, []string) {
	type key struct{ table, channel string }

	events := map[key]map[string]struct{}{}
	all := map[key]bool{}
	var warnings []string

	for i := range cfg.Metrics {
		m := &cfg.Metrics[i]
		if m.Source != source || (m.Collect.Mode != config.ModeNotify && m.Collect.Mode != config.ModeAuto) {
			continue
		}
		if m.Collect.Channel == "" {
			continue
		}
		if m.Collect.Watch == nil || len(m.Collect.Watch.Tables) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"metric %q uses channel %q but declares no collect.watch.tables; no managed trigger is generated for it",
				m.Name, m.Collect.Channel))
			continue
		}
		for _, table := range m.Collect.Watch.Tables {
			k := key{table: table, channel: m.Collect.Channel}
			if events[k] == nil {
				events[k] = map[string]struct{}{}
			}
			if len(m.Collect.Watch.Events) == 0 {
				all[k] = true
			}
			for _, e := range m.Collect.Watch.Events {
				events[k][e] = struct{}{}
			}
		}
	}

	out := make([]triggers.Subscription, 0, len(events))
	for k, set := range events {
		sub := triggers.Subscription{Table: k.table, Channel: k.channel}
		if !all[k] {
			for e := range set {
				sub.Events = append(sub.Events, e)
			}
			sort.Strings(sub.Events)
		}
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Channel < out[j].Channel
	})
	sort.Strings(warnings)
	return out, warnings
}

// Applies managed triggers after sources open and before collection starts.
//
// Failure is logged, not fatal: collection must keep working without managed
// triggers, and the plans still reconcile on fallback_interval (§9.5).
func (r *Runtime) provisionManagedTriggers(ctx context.Context) {
	for _, target := range r.triggerTargets {
		for _, w := range target.Warnings {
			r.log.Warn("managed triggers: "+w, "source", target.SourceID)
		}
		if !target.Manage {
			continue
		}

		opCtx, cancel := context.WithTimeout(ctx, triggerOperationTimeout)
		err := withTriggerManager(opCtx, target, func(m *triggers.Manager) error {
			report, err := m.Apply(opCtx, target)
			if err != nil {
				return err
			}
			r.logTriggerReport("applied", report)
			return nil
		})
		cancel()
		if err != nil {
			r.log.Error("managed trigger provisioning failed; collection continues and reconciliation repairs missed changes",
				"source", target.SourceID, "schema", target.Schema, "error", err)
		}
	}
}

// Removes managed objects on shutdown only when explicitly configured.
func (r *Runtime) cleanupManagedTriggers(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, triggerOperationTimeout)
	defer cancel()

	for _, target := range r.triggerTargets {
		if !target.Manage || !target.CleanupOnShutdown {
			continue
		}
		err := withTriggerManager(ctx, target, func(m *triggers.Manager) error {
			report, err := m.Remove(ctx, target)
			if err != nil {
				return err
			}
			r.logTriggerReport("removed", report)
			return nil
		})
		if err != nil {
			r.log.Warn("managed trigger cleanup failed",
				"source", target.SourceID, "schema", target.Schema, "error", err)
		}
	}
}

func withTriggerManager(ctx context.Context, target triggers.Target, fn func(*triggers.Manager) error) error {
	m, err := triggers.Open(ctx, target.DSN)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close(ctx) }()
	return fn(m)
}

func (r *Runtime) logTriggerReport(verb string, report triggers.Report) {
	for _, w := range report.Warnings {
		r.log.Warn("managed triggers: "+w, "source", report.Source)
	}
	if !report.Changed() {
		r.log.Info("managed triggers already current", "source", report.Source,
			"schema", report.Schema, "unchanged", report.Unchanged)
		return
	}
	for _, action := range report.Actions {
		if len(action.Statements) == 0 {
			continue
		}
		r.log.Info(verb+" managed trigger object", "source", report.Source,
			"schema", report.Schema, "action", action.Kind, "object", action.Object)
	}
}
