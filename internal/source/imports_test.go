package source_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Guards the import rule in AGENTS.md mechanically rather than by review.
//
// internal/source is the adapter contract. If it reaches the Prometheus client,
// every adapter depends transitively on an exporter concern, which is why the
// failure vocabulary lives in the dependency-free internal/telemetry/reason rather
// than in internal/telemetry (ADR 0009).
func TestSourceDoesNotDependOnExporterOrConfig(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	forbidden := []string{
		"github.com/prometheus/client_golang",
		"github.com/teagan42/metricule/internal/telemetry",
		"github.com/teagan42/metricule/internal/config",
		"github.com/teagan42/metricule/internal/secrets",
		"github.com/teagan42/metricule/internal/collect",
		"github.com/teagan42/metricule/internal/snapshot",
	}

	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range forbidden {
			// telemetry/reason is the one permitted internal dependency, and it is
			// a prefix match away from internal/telemetry.
			if dep == bad {
				t.Errorf("internal/source must not depend on %s (AGENTS.md import rules, ADR 0009)", bad)
			}
		}
	}
}
