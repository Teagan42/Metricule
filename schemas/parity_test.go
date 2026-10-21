package schemas_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	yaml "go.yaml.in/yaml/v3"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/schemas"
)

// Parity contract between the distributed JSON Schema and Go validation.
//
// The two are not equivalent and cannot be: cross-field rules such as "an http
// source requires a request block" are beyond JSON Schema's reach. The invariant
// that matters for users is directional:
//
//   - The schema MUST accept every configuration Go accepts. A schema that rejects
//     valid configuration makes editors flag working files, which is the real drift
//     risk (Agent 1 acceptance: "Schema matches Go validation").
//   - The schema MUST reject structural errors — unknown keys, wrong types, bad
//     enums, missing required fields — so editors catch them before startup.
//
// Semantic errors are Go's responsibility alone and are asserted separately.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.V1Alpha1))
	if err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("v1alpha1.schema.json", doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile("v1alpha1.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// Converts YAML to a JSON-compatible value for schema validation.
//
// A YAML mapping key of `null` resolves to the null scalar, not the string "null".
// The §5.1 contract uses that spelling for the mapping null policy, so the key is
// normalized here exactly as the Go decoder normalizes it.
func yamlToJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse YAML: %v", err)
	}
	return normalizeKeys(v)
}

func normalizeKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeKeys(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			key := "null"
			if k != nil {
				key = toString(k)
			}
			out[key] = normalizeKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeKeys(val)
		}
		return out
	default:
		return v
	}
}

func toString(k any) string {
	if s, ok := k.(string); ok {
		return s
	}
	return ""
}

func fixtures(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no fixtures in testdata/%s", dir)
	}
	return paths
}

// Asserts the schema accepts everything Go accepts.
func TestSchemaAcceptsValidConfigurations(t *testing.T) {
	sch := compileSchema(t)

	paths := append(fixtures(t, "valid"), filepath.Join("..", "examples", "reference.yaml"))
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			if _, _, err := config.Parse(data, source.CapabilitiesFor); err != nil {
				t.Fatalf("fixture is in testdata/valid but Go rejected it: %v", err)
			}
			if err := sch.Validate(yamlToJSON(t, data)); err != nil {
				t.Errorf("schema rejected a configuration Go accepts:\n%v", err)
			}
		})
	}
}

// Asserts the schema rejects structural errors, so editors catch them pre-startup.
func TestSchemaRejectsStructuralErrors(t *testing.T) {
	sch := compileSchema(t)

	for _, path := range fixtures(t, "invalid-structural") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			if err := sch.Validate(yamlToJSON(t, data)); err == nil {
				t.Error("schema accepted a structurally invalid configuration")
			}
			if _, _, err := config.Parse(data, source.CapabilitiesFor); err == nil {
				t.Error("Go accepted a structurally invalid configuration")
			}
		})
	}
}

// Documents the asymmetry: cross-field rules are Go's alone.
//
// Pinning this prevents a future contributor from assuming the schema is sufficient
// validation and skipping the Go check.
func TestSemanticErrorsAreGoOnly(t *testing.T) {
	for _, path := range fixtures(t, "invalid-semantic") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if _, _, err := config.Parse(data, source.CapabilitiesFor); err == nil {
				t.Error("Go must reject semantically invalid configuration")
			}
		})
	}
}

// The schema must declare the draft it targets (§4.2).
func TestSchemaDeclaresDraft2020_12(t *testing.T) {
	if !strings.Contains(string(schemas.V1Alpha1), "https://json-schema.org/draft/2020-12/schema") {
		t.Error("schema must declare $schema as JSON Schema Draft 2020-12")
	}
}
