# ADR 0007: JSON Schema is a subset of Go validation

Status: Accepted
Date: 2026-10-21

## Context

§5.2 requires the configuration schema to ship in the repository and release
artifacts. §4.2 specifies JSON Schema Draft 2020-12. Agent 1's acceptance criteria
include "Schema matches Go validation".

Taken literally, "matches" is unachievable. JSON Schema cannot express that a metric's
`source` names a declared source, that an HTTP source requires a `request` block
rather than a `query` block, or that `mode: notify` requires an adapter advertising
notifications. Attempting it would produce an unmaintainable schema that still missed
cases.

## Decision

Define the relationship directionally instead, and test it:

- The schema accepts every configuration Go accepts.
- The schema rejects structural errors: unknown keys, wrong types, invalid enums,
  missing required fields, malformed durations and identifiers.
- Cross-field and capability rules belong to `internal/config` alone.

`schemas/parity_test.go` enforces all three against fixture directories:
`testdata/valid`, `testdata/invalid-structural`, and `testdata/invalid-semantic`.

## Consequences

The asserted direction is the one that affects users. A schema that rejects valid
configuration makes an editor flag a working file, which erodes trust in both the
schema and the tool. A schema that accepts a semantically invalid file costs the user
nothing, because `metricule validate` and startup both still reject it.

`testdata/invalid-semantic` exists to document the gap rather than hide it, so that a
later contributor does not mistake schema validation for sufficient validation and
skip the Go path.

Because `required` in the schema must not exceed what Go requires, `collect` is
optional there: Go defaults the block when it is absent. Fields Go requires only after
defaulting cannot be marked required in the schema.

The parity test is verified to be sensitive to schema content, not merely to its
existence: loosening `additionalProperties` on the metric object fails
`TestSchemaRejectsStructuralErrors/unknown-key`.

One known wart is recorded here because it will surprise someone. §5.1 spells the
mapping null policy as `null:`, which YAML resolves to the null scalar rather than the
string `"null"`. No struct tag can express that key, so `config.Mapping` has a
hand-written unmarshaler, and the parity test normalizes the key the same way before
schema validation.
