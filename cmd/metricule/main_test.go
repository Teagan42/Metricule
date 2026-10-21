package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests run against the built binary rather than the command functions, so exit
// codes and stream routing are covered as users and container orchestrators see
// them (§15.2 requires a nonzero exit on failure).
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "metricule-cli")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	binary = filepath.Join(dir, "metricule")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build metricule: " + err.Error())
	}
	os.Exit(m.Run())
}

type result struct {
	stdout string
	stderr string
	code   int
}

func run(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()

	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return result{stdout: out.String(), stderr: errb.String(), code: code}
}

func TestVersionSucceeds(t *testing.T) {
	got := run(t, "version")
	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "metricule") {
		t.Errorf("stdout should name the program, got: %q", got.stdout)
	}
}

// An unknown command must fail rather than silently do nothing.
func TestUnknownCommandFails(t *testing.T) {
	got := run(t, "frobnicate")
	if got.code == 0 {
		t.Error("unknown command must exit nonzero")
	}
	if !strings.Contains(got.stderr, "frobnicate") {
		t.Errorf("stderr should name the unknown command, got: %q", got.stderr)
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	got := run(t)
	if got.code == 0 {
		t.Error("invoking with no command must exit nonzero")
	}
	for _, cmd := range []string{"serve", "validate", "test", "version"} {
		if !strings.Contains(got.stderr, cmd) {
			t.Errorf("usage should list %q, got: %q", cmd, got.stderr)
		}
	}
}

// `metricule config schema` must emit the schema users validate against (§15).
func TestConfigSchemaEmitsValidJSON(t *testing.T) {
	got := run(t, "config", "schema")
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", got.code, got.stderr)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want Draft 2020-12", doc["$schema"])
	}
	if _, ok := doc["$defs"]; !ok {
		t.Error("schema is missing $defs")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const validConfig = `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
  - name: app_users
    help: Current user population
    type: gauge
    source: db
    query:
      sql: SELECT COUNT(*) AS value FROM users
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`

func TestValidateAcceptsValidConfig(t *testing.T) {
	got := run(t, "validate", "--config", writeConfig(t, validConfig))
	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", got.code, got.stderr)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	got := run(t, "validate", "--config", writeConfig(t, strings.Replace(
		validConfig, "source: db", "source: missing", 1)))
	if got.code == 0 {
		t.Fatal("invalid config must exit nonzero")
	}
	if !strings.Contains(got.stderr, "metrics[0].source") {
		t.Errorf("stderr should anchor the diagnostic to a config path, got: %q", got.stderr)
	}
}

// Offline validation must not connect to upstream systems (§15.1). A config
// pointing at an unroutable address therefore still validates.
func TestValidateIsOfflineByDefault(t *testing.T) {
	got := run(t, "validate", "--config", writeConfig(t, `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://192.0.2.1:9
    timeout: 1s
metrics:
  - name: app_sessions
    help: Sessions
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.sessions"
    collect:
      mode: poll
      interval: 30s
`))
	if got.code != 0 {
		t.Errorf("offline validation must not dial the source, exit = %d (stderr: %s)", got.code, got.stderr)
	}
}

func TestValidateReportsMissingFile(t *testing.T) {
	got := run(t, "validate", "--config", filepath.Join(t.TempDir(), "absent.yaml"))
	if got.code == 0 {
		t.Error("missing config file must exit nonzero")
	}
	if !strings.Contains(got.stderr, "absent.yaml") {
		t.Errorf("stderr should name the missing path, got: %q", got.stderr)
	}
}

// Diagnostics must be identical across runs so CI failures are reproducible.
func TestValidateDiagnosticsAreDeterministic(t *testing.T) {
	path := writeConfig(t, `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  a:
    type: sqlite
    path: /data/a.db
  b:
    type: sqlite
    path: /data/b.db
metrics:
  - name: "bad name"
    help: ""
    type: nonsense
    source: nowhere
    mapping:
      value: ""
    collect:
      mode: nonsense
`)
	first := run(t, "validate", "--config", path)
	if first.code == 0 {
		t.Fatal("expected rejection")
	}
	for i := 0; i < 5; i++ {
		again := run(t, "validate", "--config", path)
		if again.stderr != first.stderr {
			t.Fatalf("diagnostics differ between runs:\nfirst:\n%s\nrun %d:\n%s", first.stderr, i, again.stderr)
		}
	}
}

// §14.3: credentials must not reach any output stream.
func TestSecretsNeverAppearInOutput(t *testing.T) {
	const secret = "sup3r-s3cret-token"
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(secret), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	path := writeConfig(t, `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
    authentication:
      type: bearer
      token:
        file: `+tokenFile+`
metrics:
  - name: app_sessions
    help: Sessions
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.sessions"
    collect:
      mode: poll
      interval: 30s
`)

	for _, args := range [][]string{
		{"validate", "--config", path},
		{"validate", "--config", path, "--print-config"},
		{"serve", "--config", path},
		{"test", "--config", path, "--metric", "app_sessions"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := run(t, args...)
			if strings.Contains(got.stdout, secret) {
				t.Error("secret leaked to stdout")
			}
			if strings.Contains(got.stderr, secret) {
				t.Error("secret leaked to stderr")
			}
		})
	}

	// serve and test resolve secrets before failing, so these exercise a DSN that
	// is actually in memory. validate is offline and never reads the credential,
	// which would make it a vacuous check.
	t.Run("resolved dsn", func(t *testing.T) {
		envPath := writeConfig(t, strings.Replace(validConfig,
			"type: sqlite\n    path: /data/app.db",
			"type: postgres\n    dsn:\n      env: TEST_DSN", 1))

		for _, args := range [][]string{
			{"serve", "--config", envPath},
			{"validate", "--config", envPath, "--online"},
			{"test", "--config", envPath, "--metric", "app_users"},
		} {
			cmd := exec.Command(binary, args...)
			cmd.Env = append(os.Environ(), "TEST_DSN=postgres://user:"+secret+"@db/app")
			out, _ := cmd.CombinedOutput()
			if bytes.Contains(out, []byte(secret)) {
				t.Errorf("%v leaked a resolved DSN password:\n%s", args, out)
			}
		}
	})
}

// M0 has no adapters linked. serve must say so plainly rather than start and
// expose an empty /metrics, which would look like a working exporter (rule 4).
func TestServeReportsMissingAdapter(t *testing.T) {
	got := run(t, "serve", "--config", writeConfig(t, validConfig))
	if got.code == 0 {
		t.Fatal("serve must exit nonzero when no adapter can satisfy the config")
	}
	if !strings.Contains(got.stderr, "sqlite") {
		t.Errorf("stderr should name the unsatisfied source type, got: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "not implemented") {
		t.Errorf("stderr should state the adapter is absent from this build, got: %q", got.stderr)
	}
}

func TestTestCommandRequiresKnownMetric(t *testing.T) {
	got := run(t, "test", "--config", writeConfig(t, validConfig), "--metric", "no_such_metric")
	if got.code == 0 {
		t.Fatal("unknown metric must exit nonzero")
	}
	if !strings.Contains(got.stderr, "no_such_metric") {
		t.Errorf("stderr should name the unknown metric, got: %q", got.stderr)
	}
}
