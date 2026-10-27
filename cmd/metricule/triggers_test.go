package main_test

import (
	"strings"
	"testing"
)

func TestTriggersRequiresASubcommand(t *testing.T) {
	got := run(t, "triggers")
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "expected subcommand") {
		t.Errorf("stderr = %q", got.stderr)
	}
}

func TestTriggersRejectsAnUnknownSubcommand(t *testing.T) {
	got := run(t, "triggers", "install")
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, `unknown subcommand "install"`) {
		t.Errorf("stderr = %q", got.stderr)
	}
}

func TestTriggersRequiresConfig(t *testing.T) {
	got := run(t, "triggers", "plan")
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "--config is required") {
		t.Errorf("stderr = %q", got.stderr)
	}
}

// Provisioning must never happen without the explicit opt-in (AD-006).
func TestTriggersRefusesToProvisionWithoutManage(t *testing.T) {
	path := writeConfig(t, `
apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  appdb:
    type: postgres
    dsn:
      value: postgres://u:p@127.0.0.1:1/app
    triggers:
      manage: false
      schema: metricule
metrics:
  - name: app_jobs
    help: Queued jobs
    type: gauge
    source: appdb
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`)

	got := run(t, "triggers", "plan", "--config", path)
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "triggers.manage is false") {
		t.Errorf("stderr = %q", got.stderr)
	}
}

func TestTriggersRequiresAConfiguredSource(t *testing.T) {
	path := writeConfig(t, `
apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  appdb:
    type: postgres
    dsn:
      value: postgres://u:p@127.0.0.1:1/app
metrics:
  - name: app_jobs
    help: Queued jobs
    type: gauge
    source: appdb
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`)

	got := run(t, "triggers", "status", "--config", path)
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "no source configures triggers") {
		t.Errorf("stderr = %q", got.stderr)
	}
}
