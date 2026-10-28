package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/secrets"
	"github.com/teagan42/metricule/internal/source"
)

func TestBuildSourceTranslatesMySQL(t *testing.T) {
	src, err := BuildSource("appdb", config.SourceConfig{
		Type: config.SourceMySQL,
		MySQL: &config.MySQLSource{
			DSN:          secrets.Ref{Value: "metricule:secret@tcp(db.invalid:3306)/app"},
			Pool:         config.Pool{MaxOpen: 3, MaxIdle: 1},
			QueryTimeout: config.Duration(2 * time.Second),
		},
	})
	if err != nil {
		t.Fatalf("BuildSource: %v", err)
	}
	if src.Type() != source.TypeMySQL {
		t.Errorf("type = %q", src.Type())
	}
	want := source.Capabilities{Query: true, Poll: true}
	if got := src.Capabilities(); got != want {
		t.Errorf("capabilities = %+v, want %+v", got, want)
	}
}

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
