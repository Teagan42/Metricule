package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/config"
)

// An auto plan whose source cannot observe change must say it is polling rather
// than degrade silently.
func TestAutoWarnsForASourceWithoutObservation(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	m := &config.Metric{
		Name:   "app_users",
		Source: "plain",
		Collect: config.Collect{
			Mode:             config.ModeAuto,
			FallbackInterval: config.Duration(time.Minute),
		},
	}
	if err := resolveMode(log, m, &fakeSource{id: "plain"}); err != nil {
		t.Fatalf("resolveMode: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "collection mode resolved to polling") {
		t.Fatalf("no degraded-mode warning logged: %s", out)
	}
}

// SQLite observes change tokens, so auto must report that mechanism.
func TestSQLiteAutoResolvesToChangeTokens(t *testing.T) {
	src, err := BuildSource("app", config.SourceConfig{
		Type:   config.SourceSQLite,
		SQLite: &config.SQLiteSource{Path: "/tmp/absent.db", BusyTimeout: config.Duration(3 * time.Second)},
	})
	if err != nil {
		t.Fatalf("BuildSource: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	m := &config.Metric{
		Name:   "app_users",
		Source: "app",
		Collect: config.Collect{
			Mode:             config.ModeAuto,
			FallbackInterval: config.Duration(time.Minute),
		},
	}
	if err := resolveMode(log, m, src); err != nil {
		t.Fatalf("resolveMode: %v", err)
	}

	if out := buf.String(); !strings.Contains(out, "resolved to change tokens") {
		t.Fatalf("no mode-resolution message: %s", out)
	}
}

// require_notifications turns a sanctioned downgrade into a startup failure.
func TestRequireNotificationsFailsWhenTheSourceCannotDeliver(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	for _, mode := range []config.CollectMode{config.ModeNotify, config.ModeAuto} {
		m := &config.Metric{
			Name:   "app_users",
			Source: "plain",
			Collect: config.Collect{
				Mode:                 mode,
				RequireNotifications: true,
				FallbackInterval:     config.Duration(time.Minute),
			},
		}
		if err := resolveMode(log, m, &fakeSource{id: "plain"}); err == nil {
			t.Errorf("mode %q accepted require_notifications on a source without notifications", mode)
		}
	}
}

// A source that delivers notifications must not be reported as degraded.
func TestNotifyOnANotifyingSourceIsNotDegraded(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	m := &config.Metric{
		Name:   "app_users",
		Source: "app",
		Collect: config.Collect{
			Mode:                 config.ModeNotify,
			FallbackInterval:     config.Duration(time.Minute),
			RequireNotifications: true,
		},
	}
	src := &fakeWatchable{id: "app"}
	if err := resolveMode(log, m, src); err != nil {
		t.Fatalf("resolveMode: %v", err)
	}
	if out := buf.String(); out != "" {
		t.Fatalf("unexpected warning: %s", out)
	}
}
