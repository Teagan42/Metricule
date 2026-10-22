package httpjson_test

import (
	"testing"

	"github.com/teagan42/metricule/internal/source"
	_ "github.com/teagan42/metricule/internal/source/httpjson"
)

func TestFactoryBuildsIdentifiedSource(t *testing.T) {
	t.Parallel()

	s, err := source.New(source.Settings{
		ID:   "appapi",
		Type: source.TypeHTTP,
		Raw:  &source.HTTPSettings{BaseURL: "http://127.0.0.1:9/"},
	})
	if err != nil {
		t.Fatalf("source.New: %v", err)
	}
	if got := s.ID(); got != "appapi" {
		t.Errorf("ID() = %q, want %q", got, "appapi")
	}
	if got := s.Type(); got != source.TypeHTTP {
		t.Errorf("Type() = %q, want %q", got, source.TypeHTTP)
	}
	caps := s.Capabilities()
	if !caps.Query || !caps.Poll {
		t.Errorf("Capabilities() = %+v, want Query and Poll", caps)
	}
	if caps.Notifications || caps.ChangeTokens || caps.CDC || caps.TriggerManagement {
		t.Errorf("Capabilities() = %+v, want no change observation", caps)
	}
}

func TestFactoryRejectsForeignSettings(t *testing.T) {
	t.Parallel()

	if _, err := source.New(source.Settings{
		ID:   "appapi",
		Type: source.TypeHTTP,
		Raw:  &source.SQLiteSettings{Path: "/tmp/x.db"},
	}); err == nil {
		t.Fatal("expected error for non-HTTP settings")
	}
}

func TestFactoryRejectsInvalidBaseURL(t *testing.T) {
	t.Parallel()

	for name, base := range map[string]string{
		"empty":          "",
		"no scheme":      "app:8080",
		"wrong scheme":   "ftp://app:8080",
		"no host":        "http://",
		"unparseable":    "http://[::1",
		"with userinfo":  "http://user:pass@app:8080",
		"with fragment":  "http://app:8080/#frag",
		"with raw query": "http://app:8080/?a=b",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := source.New(source.Settings{
				ID:   "appapi",
				Type: source.TypeHTTP,
				Raw:  &source.HTTPSettings{BaseURL: base},
			}); err == nil {
				t.Fatalf("expected error for base_url %q", base)
			}
		})
	}
}
