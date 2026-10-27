package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// A reference must never reveal its value through formatting or serialization.
func TestRefNeverRendersItsSecret(t *testing.T) {
	const secret = "s3cret-value"
	ref := Ref{Value: secret}

	cases := map[string]string{
		"String": ref.String(),
	}
	yamlBytes, err := yaml.Marshal(ref)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	cases["YAML"] = string(yamlBytes)
	jsonBytes, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	cases["JSON"] = string(jsonBytes)

	for name, got := range cases {
		if strings.Contains(got, secret) {
			t.Errorf("%s leaked the secret: %q", name, got)
		}
		if !strings.Contains(got, "literal") || !strings.Contains(got, Redacted) {
			t.Errorf("%s = %q, want the reference kind and redaction marker", name, got)
		}
	}
}

func TestRefDescribesIndirectSources(t *testing.T) {
	if got := (Ref{Env: "DATABASE_URL"}).String(); got != "env:DATABASE_URL" {
		t.Errorf("env ref = %q", got)
	}
	if got := (Ref{File: "/run/secrets/db"}).String(); got != "file:/run/secrets/db" {
		t.Errorf("file ref = %q", got)
	}
	if got := (Ref{}).String(); got != "unset" {
		t.Errorf("zero ref = %q", got)
	}
}

func TestResolveEachKind(t *testing.T) {
	if got, err := (Ref{Value: "from-value"}).Resolve(); err != nil || got != "from-value" {
		t.Errorf("value ref = %q, %v", got, err)
	}

	t.Setenv("METRICULE_TEST_SECRET", "from-env")
	if got, err := (Ref{Env: "METRICULE_TEST_SECRET"}).Resolve(); err != nil || got != "from-env" {
		t.Errorf("env ref = %q, %v", got, err)
	}

	// Docker secrets and kubectl append a newline; it is not part of the value.
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if got, err := (Ref{File: path}).Resolve(); err != nil || got != "from-file" {
		t.Errorf("file ref = %q, %v", got, err)
	}
}

func TestResolveRejectsMissingOrEmpty(t *testing.T) {
	if _, err := (Ref{Env: "METRICULE_DEFINITELY_UNSET"}).Resolve(); err == nil {
		t.Error("missing environment variable resolved")
	}

	t.Setenv("METRICULE_EMPTY_SECRET", "")
	if _, err := (Ref{Env: "METRICULE_EMPTY_SECRET"}).Resolve(); err == nil {
		t.Error("empty environment variable resolved")
	}

	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := (Ref{File: path}).Resolve(); err == nil {
		t.Error("empty secret file resolved")
	}
}

func TestValidateRejectsEmptyAndAmbiguous(t *testing.T) {
	if err := (Ref{}).Validate(); err == nil {
		t.Error("empty reference accepted")
	}
	if err := (Ref{Value: "a", Env: "B"}).Validate(); err == nil {
		t.Error("ambiguous reference accepted")
	}
	if err := (Ref{Value: "a"}).Validate(); err != nil {
		t.Errorf("single reference rejected: %v", err)
	}
}

// DSNs reach logs and errors; credentials must never survive redaction.
func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "url password",
			in:   "postgres://metricule:s3cret@db.internal:5432/app",
			want: "postgres://metricule:%5BREDACTED%5D@db.internal:5432/app",
		},
		{
			name: "url without password",
			in:   "postgres://metricule@db.internal/app",
			want: "postgres://metricule@db.internal/app",
		},
		{
			name: "query password",
			in:   "postgres://db.internal/app?password=s3cret&sslmode=require",
			want: "postgres://db.internal/app?password=%5BREDACTED%5D&sslmode=require",
		},
		{
			name: "key-value password",
			in:   "host=db.internal user=metricule password=s3cret dbname=app",
			want: "host=db.internal user=metricule password=[REDACTED] dbname=app",
		},
		{
			name: "key-value sslkey",
			in:   "host=db sslkey=/run/secrets/client.key",
			want: "host=db sslkey=[REDACTED]",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "unparseable",
			in:   "this is not a dsn but may contain s3cret",
			want: Redacted,
		},
	}
	for _, tc := range cases {
		if got := RedactDSN(tc.in); got != tc.want {
			t.Errorf("%s: RedactDSN = %q, want %q", tc.name, got, tc.want)
		}
	}
}
