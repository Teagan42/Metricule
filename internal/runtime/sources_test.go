package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/config"
)

// A trigger block that provisions nothing must not pass startup silently.
func TestManagedTriggersWarnThatTheyAreIgnored(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	warnIgnoredTriggerManagement(log, triggerConfig(true))

	out := buf.String()
	if !strings.Contains(out, "triggers.manage is ignored until M3") {
		t.Fatalf("no warning logged: %s", out)
	}
	if !strings.Contains(out, `"source":"appdb"`) {
		t.Fatalf("warning does not identify the source: %s", out)
	}
}

func TestTriggerWarningIsAbsentWhenManagementIsOff(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	warnIgnoredTriggerManagement(log, triggerConfig(false))

	if out := buf.String(); out != "" {
		t.Fatalf("unexpected warning: %s", out)
	}
}

// SQLite advertises change tokens at the type level, but this build cannot observe
// them. An auto plan must say it is polling rather than degrade silently.
func TestSQLiteAutoWarnsThatItPolls(t *testing.T) {
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
	warnDegradedMode(log, m, src)

	out := buf.String()
	if !strings.Contains(out, "collection mode resolved to polling") {
		t.Fatalf("no degraded-mode warning logged: %s", out)
	}
}

func triggerConfig(manage bool) *config.Config {
	return &config.Config{
		Sources: map[string]config.SourceConfig{
			"appdb": {
				Type: config.SourcePostgres,
				Postgres: &config.PostgresSource{
					Triggers: &config.Triggers{Manage: manage, Schema: "metricule"},
				},
			},
		},
	}
}
