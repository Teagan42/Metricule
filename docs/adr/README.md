# Architecture Decision Records

Each record states a decision, why it was made, and what it costs. Records are
immutable: supersede a record with a new one rather than editing it.

Per `AGENTS.md`, decisions that change a shared interface need Agent 0's review and a
new record here.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-settled-baseline-decisions.md) | Settled baseline decisions (AD-001 to AD-015) | Accepted |
| [0002](0002-dependency-selection.md) | Dependency selection and pinning | Accepted |
| [0003](0003-rfc-9535-json-selectors.md) | JSON selectors are RFC 9535 JSONPath | Accepted |
| [0004](0004-cgo-disabled-by-default.md) | CGO disabled by default | Accepted |
| [0005](0005-capability-declaration-split.md) | Capability declaration is separate from adapter registration | Accepted |
| [0006](0006-stdlib-cli.md) | Standard library flag package instead of a CLI framework | Accepted |
| [0007](0007-schema-validation-parity.md) | JSON Schema is a subset of Go validation | Accepted |
| [0008](0008-document-adapters-resolve-selectors.md) | Document adapters resolve selectors, not the mapping engine | Accepted |
| [0009](0009-adapter-error-and-diagnostic-contracts.md) | Adapter error classification and source diagnostics | Accepted |
| [0010](0010-managed-trigger-ownership.md) | Managed trigger ownership and shape | Accepted |

## Format

```markdown
# ADR NNNN: Title

Status: Proposed | Accepted | Superseded by ADR-NNNN
Date: YYYY-MM-DD

## Context
What forced a decision.

## Decision
What was decided, in the active voice.

## Consequences
What this buys, what it costs, and what it rules out.
```
