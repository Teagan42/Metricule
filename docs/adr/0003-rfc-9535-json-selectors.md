# ADR 0003: JSON selectors are RFC 9535 JSONPath

Status: Accepted
Date: 2026-10-21

## Context

§4.2 permits "a maintained JSONPath or GJSON-compatible library". §11.2 is stricter
about the outcome: "JSON selector semantics MUST be precisely documented and backed by
tests", behaving consistently across nested arrays, missing fields, null values, and
numeric conversions.

The §5.1 and §11.2 configuration examples use `$.sessions.active` and `$.workers[*]`
— JSONPath syntax, not GJSON's `sessions.active`.

Two candidates were considered: `tidwall/gjson` (v1.20.0) and `theory/jsonpath`
(v0.12.2).

## Decision

Use `github.com/theory/jsonpath`, which implements RFC 9535, and wrap it in
`internal/selector`.

Configuration continues to use the `$.`-prefixed form the specification documents.

## Consequences

The documentation obligation in §11.2 is discharged by citing a standard rather than
describing a dialect. Edge cases — descendant segments, filter expressions, array
slices — have normative answers we do not have to invent or test exhaustively
ourselves.

Choosing GJSON would have meant either changing the documented configuration syntax,
or translating `$.`-prefixed JSONPath into GJSON paths. That translation is where the
precision would have been lost: `$.workers[*]` and `workers.#` differ on absent keys,
and a half-implemented JSONPath is exactly the bespoke dialect §11.2 forbids.

The cost is a v0.x dependency with no API stability promise. `internal/selector` is
the containment: it exposes `Parse`, `Valid`, `SelectOne`, and `SelectAll`, so a
replacement touches one file rather than config, mapping, and the HTTP adapter.

`SelectOne` deliberately fails on multiple matches rather than taking the first. A
sample value that depended on JSON member ordering would be unreproducible, and
"first match" has no meaning for an unordered object.
