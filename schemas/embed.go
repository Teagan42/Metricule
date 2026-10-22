// Package schemas distributes Metricule's configuration JSON Schema.
//
// The schema is a release artifact (§5.2): editors and CI consume it directly from
// the repository path, so the canonical file stays at schemas/v1alpha1.schema.json
// and is embedded from beside this file rather than copied elsewhere.
//
// The schema catches structural errors only. Cross-field and capability rules live in
// internal/config, and schemas/parity_test.go pins the boundary between them.
package schemas

import _ "embed"

// V1Alpha1 is the JSON Schema for apiVersion metricule.io/v1alpha1.
//
//go:embed v1alpha1.schema.json
var V1Alpha1 []byte
