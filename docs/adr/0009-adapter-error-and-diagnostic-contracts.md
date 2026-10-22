# ADR 0009: Adapter error classification and source diagnostics

Status: Accepted
Date: 2026-10-22

## Context

Three M1 agents hit the same wall from different directions.

Agents 2 and 5 reported that an adapter cannot tell the scheduler *why* a collection
failed. The adapter cannot import `internal/collect` (rule 5), and the scheduler must
not type-assert concrete adapter types (P7), so every adapter-specific failure
collapsed to `reason="unknown"` and Agent 5's `ErrTooManyRows` could not reach
`telemetry.ReasonResultTooLarge`.

Agent 4 then reported that a convention alone is not enough: an adapter spelling the
method differently reports `unknown` with no build error, and the convention is
discoverable only by reading the scheduler's classifier.

Separately, `metricule_source_up` and `metricule_source_reconnects_total` had no
owner. Adapters count reconnections but receive no `*telemetry.Metrics` — and should
not, since `source.Settings` carries no telemetry handle — while the scheduler holds
no source handle and runs no health loop.

## Decision

Add two contracts to `internal/source`:

```go
type Classified interface {
    error
    Reason() telemetry.Reason
}

type Diagnosable interface {
    Diagnostics() Diagnostics
}

type Diagnostics struct {
    Reconnects uint64
}
```

`ReasonOf(err)` traverses wrapping with `errors.As` and falls back to
`ReasonUnknown`. Consumers keep their own allowlist: a reason outside the set a
consumer accepts collapses to `unknown` rather than widening the label space (§13).

`internal/runtime` owns the periodic health loop and publishes both source-level
families, reading reconnect totals through `Diagnosable` and adding deltas.

This relaxes the dependency rule from `source -> (nothing internal)` to
`source -> telemetry`.

## Consequences

The relaxed import is safe, and the reason is worth recording because the original
rule reads like an absolute. `telemetry` is a leaf — it imports only
`internal/version` — so `config -> source -> telemetry` stays acyclic. The rule
existed to keep `config` and `source` from cycling, which is what makes offline
capability validation possible (ADR 0005); it was never an end in itself.

The alternative was duplicating the `Reason` enum in `source` and translating. That
compromise already exists once: `snapshot` mirrors `config.FailureMode` because
`snapshot` genuinely must import nothing. Once is a tolerable wart; twice would make
enum drift the normal state.

Declaring the interface rather than relying on a convention buys a compile-time
check. An adapter error that does not satisfy `Classified` fails an assignment rather
than silently degrading telemetry, and the contract is discoverable where adapters
already look.

`Diagnostics` keeps adapters free of telemetry plumbing, at the cost of a polled
counter rather than a pushed one. Totals are monotonic for the life of the source and
the publisher tracks deltas, so a reconnect cannot make the counter move backwards.

Both interfaces are optional. A source that implements neither reports `unknown`
reasons and no reconnect count, which is the current behavior and degrades visibly
rather than silently.
