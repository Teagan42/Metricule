package postgres

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

func TestPoolConfigAppliesBounds(t *testing.T) {
	s := source.PostgresSettings{
		DSN:          "postgres://u:p@localhost:5432/app",
		MaxOpen:      7,
		MaxIdle:      3,
		QueryTimeout: 1500 * time.Millisecond,
	}
	cfg, err := poolConfig(s)
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want 7", cfg.MaxConns)
	}
	if cfg.MinConns != 3 {
		t.Errorf("MinConns = %d, want max_idle 3", cfg.MinConns)
	}
	if cfg.MaxConnIdleTime != defaultConnIdleTime {
		t.Errorf("MaxConnIdleTime = %s, want %s", cfg.MaxConnIdleTime, defaultConnIdleTime)
	}
	if cfg.ConnConfig.ConnectTimeout != defaultConnectTimeout {
		t.Errorf("ConnectTimeout = %s, want %s", cfg.ConnConfig.ConnectTimeout, defaultConnectTimeout)
	}
}

// §14.5: read-only enforcement must not depend on reading SQL text.
func TestPoolConfigForcesReadOnlySessions(t *testing.T) {
	cfg, err := poolConfig(source.PostgresSettings{
		DSN:          "postgres://u:p@localhost/app?default_transaction_read_only=off",
		MaxOpen:      1,
		QueryTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	params := cfg.ConnConfig.RuntimeParams
	if got := params["default_transaction_read_only"]; got != "on" {
		t.Errorf("default_transaction_read_only = %q, want \"on\" even when the DSN disables it", got)
	}
	if got := params["statement_timeout"]; got != "1000" {
		t.Errorf("statement_timeout = %q, want 1000", got)
	}
	if got := params["application_name"]; got != applicationName {
		t.Errorf("application_name = %q, want %q", got, applicationName)
	}
}

func TestPoolConfigKeepsExplicitRuntimeParams(t *testing.T) {
	cfg, err := poolConfig(source.PostgresSettings{
		DSN:          "postgres://u:p@localhost/app?statement_timeout=250&application_name=custom",
		MaxOpen:      1,
		QueryTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "250" {
		t.Errorf("statement_timeout = %q, want the DSN value 250", got)
	}
	if got := cfg.ConnConfig.RuntimeParams["application_name"]; got != "custom" {
		t.Errorf("application_name = %q, want the DSN value", got)
	}
}

// Settings override an sslmode already present in the DSN.
func TestWithTLSParamsURLForm(t *testing.T) {
	got := withTLSParams("postgres://u:p@localhost:5432/app?sslmode=prefer", source.PostgresSettings{
		TLSMode:     "verify-full",
		TLSRootCert: "/certs/ca.pem",
		TLSCert:     "/certs/client.pem",
		TLSKey:      "/certs/client key.pem",
	})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	want := map[string]string{
		"sslmode":     "verify-full",
		"sslrootcert": "/certs/ca.pem",
		"sslcert":     "/certs/client.pem",
		"sslkey":      "/certs/client key.pem",
	}
	for k, v := range want {
		if u.Query().Get(k) != v {
			t.Errorf("%s = %q, want %q", k, u.Query().Get(k), v)
		}
	}
}

func TestPoolConfigBuildsVerifyingTLS(t *testing.T) {
	cfg, err := poolConfig(source.PostgresSettings{
		DSN:          "postgres://u:p@localhost:5432/app",
		TLSMode:      "verify-full",
		MaxOpen:      1,
		QueryTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if cfg.ConnConfig.TLSConfig == nil {
		t.Fatal("verify-full produced no TLS configuration")
	}
	if cfg.ConnConfig.TLSConfig.InsecureSkipVerify {
		t.Error("verify-full must verify the server certificate")
	}
	if cfg.ConnConfig.TLSConfig.ServerName != "localhost" {
		t.Errorf("ServerName = %q, want localhost", cfg.ConnConfig.TLSConfig.ServerName)
	}
}

func TestWithTLSParamsKeywordForm(t *testing.T) {
	got := withTLSParams("host=localhost user=u dbname=app", source.PostgresSettings{
		TLSMode:     "require",
		TLSRootCert: "/certs/o'brien.pem",
	})
	if !strings.Contains(got, "sslmode='require'") {
		t.Errorf("%q does not carry sslmode", got)
	}
	if !strings.Contains(got, `sslrootcert='/certs/o\'brien.pem'`) {
		t.Errorf("%q does not escape the quoted path", got)
	}
}

func TestWithTLSParamsLeavesDSNAloneWhenUnset(t *testing.T) {
	const dsn = "postgres://u:p@localhost/app"
	if got := withTLSParams(dsn, source.PostgresSettings{}); got != dsn {
		t.Errorf("withTLSParams = %q, want the DSN unchanged", got)
	}
}

// §14.3: a parse failure must not echo the connection string.
func TestPoolConfigErrorsOmitCredentials(t *testing.T) {
	_, err := poolConfig(source.PostgresSettings{
		DSN: "postgres://user:hunter2@%zz/app", MaxOpen: 1, QueryTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaked the password: %q", err)
	}
}

func TestDescribeTargetOmitsCredentials(t *testing.T) {
	cfg, err := poolConfig(source.PostgresSettings{
		DSN: "postgres://metricule:hunter2@db.internal:6543/app", MaxOpen: 1, QueryTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	got := describeTarget(cfg)
	if got != "db.internal:6543/app" {
		t.Errorf("describeTarget = %q, want db.internal:6543/app", got)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "metricule") {
		t.Errorf("describeTarget leaked credentials: %q", got)
	}
}
