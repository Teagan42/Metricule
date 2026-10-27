package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/secrets"
)

// Trigger targets are derived from change-driven metrics, not from the source
// block alone, because a trigger is per table and channel.
func TestTriggerTargetsMergeTableChannelsAcrossMetrics(t *testing.T) {
	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"appdb": {
				Type: config.SourcePostgres,
				Postgres: &config.PostgresSource{
					DSN:      secrets.Ref{Value: "postgres://u:p@db.invalid/app"},
					Triggers: &config.Triggers{Manage: true, Schema: "metricule"},
				},
			},
		},
		Metrics: []config.Metric{
			{
				Name: "jobs", Source: "appdb",
				Collect: config.Collect{
					Mode:    config.ModeNotify,
					Channel: "changes",
					Watch:   &config.Watch{Tables: []string{"public.jobs"}, Events: []string{"insert"}},
				},
			},
			{
				Name: "jobs_by_state", Source: "appdb",
				Collect: config.Collect{
					Mode:    config.ModeNotify,
					Channel: "changes",
					Watch:   &config.Watch{Tables: []string{"public.jobs"}, Events: []string{"update", "insert"}},
				},
			},
		},
	}

	targets, err := BuildTriggerTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTriggerTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	target := targets[0]
	if !target.Manage || target.Schema != "metricule" {
		t.Errorf("target = %+v, want manage true in metricule", target)
	}
	if len(target.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %+v, want one table/channel pair", target.Subscriptions)
	}
	sub := target.Subscriptions[0]
	if sub.Table != "public.jobs" || sub.Channel != "changes" {
		t.Errorf("subscription = %+v", sub)
	}
	if len(sub.Events) != 2 || sub.Events[0] != "insert" || sub.Events[1] != "update" {
		t.Errorf("events = %v, want the union insert,update", sub.Events)
	}
}

// A missing explicit event list means all events and must stay that way when
// merged with another metric.
func TestTriggerTargetsUnionTreatsAnEmptyEventListAsAll(t *testing.T) {
	cfg := triggerTestConfig(true)
	cfg.Metrics = []config.Metric{
		{Name: "a", Source: "appdb", Collect: config.Collect{
			Mode: config.ModeNotify, Channel: "changes",
			Watch: &config.Watch{Tables: []string{"public.jobs"}, Events: []string{"insert"}},
		}},
		{Name: "b", Source: "appdb", Collect: config.Collect{
			Mode: config.ModeNotify, Channel: "changes",
			Watch: &config.Watch{Tables: []string{"public.jobs"}},
		}},
	}

	targets, err := BuildTriggerTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTriggerTargets: %v", err)
	}
	if events := targets[0].Subscriptions[0].Events; len(events) != 0 {
		t.Errorf("events = %v, want empty (all)", events)
	}
}

func TestTriggerTargetsIncludeStatusTargetsWhenManageIsOff(t *testing.T) {
	cfg := triggerTestConfig(false)
	cfg.Metrics = []config.Metric{{
		Name: "jobs", Source: "appdb",
		Collect: config.Collect{
			Mode: config.ModeNotify, Channel: "changes",
			Watch: &config.Watch{Tables: []string{"public.jobs"}},
		},
	}}

	targets, err := BuildTriggerTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTriggerTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Manage {
		t.Fatalf("targets = %+v, want one status-only target", targets)
	}
}

func TestTriggerTargetsWarnAboutMetricsWithoutTables(t *testing.T) {
	cfg := triggerTestConfig(true)
	cfg.Metrics = []config.Metric{{
		Name: "jobs", Source: "appdb",
		Collect: config.Collect{
			Mode: config.ModeNotify, Channel: "changes",
			FallbackInterval: config.Duration(time.Minute),
		},
	}}

	targets, err := BuildTriggerTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTriggerTargets: %v", err)
	}
	if len(targets[0].Subscriptions) != 0 {
		t.Errorf("subscriptions = %+v, want none", targets[0].Subscriptions)
	}
	if len(targets[0].Warnings) != 1 || !strings.Contains(targets[0].Warnings[0], "no collect.watch.tables") {
		t.Errorf("warnings = %v, want a missing-tables warning", targets[0].Warnings)
	}
}

// Polling metrics never generate triggers.
func TestTriggerTargetsIgnorePollingMetrics(t *testing.T) {
	cfg := triggerTestConfig(true)
	cfg.Metrics = []config.Metric{{
		Name: "jobs", Source: "appdb",
		Collect: config.Collect{Mode: config.ModePoll, Interval: config.Duration(time.Minute)},
	}}

	targets, err := BuildTriggerTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTriggerTargets: %v", err)
	}
	if len(targets[0].Subscriptions) != 0 {
		t.Errorf("subscriptions = %+v, want none", targets[0].Subscriptions)
	}
}

// The provisioning DSN takes precedence over the collection DSN.
func TestTriggerTargetsPreferTheProvisioningDSN(t *testing.T) {
	cfg := triggerTestConfig(true)
	cfg.Sources["appdb"].Postgres.Triggers.DSN = secrets.Ref{Value: "postgres://owner:secret@db.invalid/app"}

	targets, err := BuildTriggerTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTriggerTargets: %v", err)
	}
	if !strings.Contains(targets[0].DSN, "owner") {
		t.Errorf("DSN did not resolve the provisioning reference")
	}
}

func triggerTestConfig(manage bool) *config.Config {
	return &config.Config{
		Sources: map[string]config.SourceConfig{
			"appdb": {
				Type: config.SourcePostgres,
				Postgres: &config.PostgresSource{
					DSN:      secrets.Ref{Value: "postgres://u:p@db.invalid/app"},
					Triggers: &config.Triggers{Manage: manage, Schema: "metricule"},
				},
			},
		},
	}
}
