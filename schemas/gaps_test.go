//go:build configgaps

// Failing tests for ADR 0007 direction breaks.
//
// Run with: go test -tags configgaps ./schemas/
//
// ADR 0007 asserts one direction: the schema accepts every configuration Go accepts.
// The fixtures in testdata/gap violate it — Go accepts them, the schema rejects them —
// because ApplyDefaults treats an explicit zero or an explicit `readonly: false` as
// unset and rewrites it. Fixing the Go side closes both these tests and the ones in
// internal/config/gaps_test.go.
//
// Build-tagged so `make check` stays green. testdata/gap is a separate directory so
// the always-on parity test's globs do not pick it up.
package schemas_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

func TestSchemaAcceptsEverythingGoAccepts(t *testing.T) {
	sch := compileSchema(t)

	for _, path := range fixtures(t, "gap") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			if _, _, err := config.Parse(data, source.CapabilitiesFor); err != nil {
				t.Skipf("Go now rejects this fixture, so the direction holds: %v", err)
			}
			if err := sch.Validate(yamlToJSON(t, data)); err != nil {
				t.Errorf("schema rejected a configuration Go accepts (ADR 0007):\n%v", err)
			}
		})
	}
}
