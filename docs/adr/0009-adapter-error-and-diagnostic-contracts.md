# ADR 0009: Adapter error classification and source diagnostics

Status: Accepted
Date: 2026-10-22

## Context

Three M1 agents hit the same wall from different directions.

Agents 2 and 5 reported that an adapter cannot tell the scheduler *why* a collection
failed. The adapter cannot import `internal/collect` (rule 5), and the scheduler must
not type-assert concrete adapter types (P7), so every adapter-specific failure
collapsed to `reason="unknown"` and Agent 5's `ErrTooManyRows` could not reach
`ReasonResultTooLarge`.

Agent 4 added that a convention alone is not enough: an adapter spelling the method
differently reports `unknown` with no build error, and the convention is discoverable
only by reading the scheduler's classifier.

Separately, `metricule_source_up` and `metricule_source_reconnects_total` had no
owner. Adapters count reconnections but receive no telemetry handle — and should not,
since `source.Settings` carries none — while the scheduler holds no source handle and
runs no health loop.

## Decision

Add to `internal/source`:

```go
type Classified interface {
    error
    Reason() reason.Reason
}

type Diagnosable interface {
    Diagnostics() Diagnostics
}
```

`ReasonOf(err) (reason.Reason, bool)` traverses wrapping with `errors.As`. The
boolean reports whether an adapter actually classified the error. Consumers keep
their own allowlist: a reason outside the set a consumer accepts collapses to
`unknown` rather than widening the label space (§13).

The failure vocabulary moves to **`internal/telemetry/reason`**, a dependency-free
leaf package. `telemetry.Reason` becomes a type alias, so both spellings name the
same type and existing callers are unaffected.

`Diagnostics` carries an adapter's full health state rather than only a reconnect
count:

```go
type Diagnostics struct {
    Target              string          // credential-free, loggable
    Up                  bool
    CheckedAt           time.Time       // zero until the first check
    LastSuccess         time.Time       // zero until the first success
    ConsecutiveFailures int
    Reconnects          uint64          // monotonic for the source's lifetime
    LastError           error           // retained after recovery
    Reason              reason.Reason
}
```

`internal/runtime` owns the periodic health loop and publishes both source-level
families, deriving a counter delta from `Reconnects`.

## Consequences

The first draft of this decision relaxed the dependency rule to `source -> telemetry`
and justified it on the grounds that `telemetry` is a leaf, so the graph stays
acyclic. Agent 2 pointed out that acyclicity was never the real cost: `telemetry`
imports `client_golang`, so that edge would have made **every adapter transitively
depend on the Prometheus client**, turning `internal/source` from a pure data
contract into one carrying an exporter concern.

That critique was correct, and it changed the decision rather than just its wording.
Extracting the vocabulary into a dependency-free package removes the coupling instead
of documenting it. Verified:

```
$ go list -deps ./internal/source | grep metricule
internal/telemetry/reason
internal/source
```

`client_golang` is not reachable from `internal/source`. An adapter depends on the
label vocabulary, which is a genuine shared contract, and on nothing else.

The alternative considered and rejected was declaring a separate reason type in
`source` and mapping it in `collect`. That is enum duplication, a compromise
`snapshot` already makes once with `config.FailureMode`; twice would make enum drift
the normal state.

Declaring the interface rather than relying on a convention buys a compile-time
check: an adapter error that does not satisfy `Classified` fails an assignment
instead of silently degrading telemetry.

The wide `Diagnostics` is Agent 4's proposal, taken over a minimal
`{Reconnects uint64}`. The fields are not adapter-specific — every source has
reachability, a last error, a failure streak, and a loggable target — and a single
shared type avoids a `postgres.Diagnostics` / `source.Diagnostics` name collision.
It also gives `/readyz` and `metricule test` the uniform per-source detail §15.2 asks
for, which a reconnect count alone could not. The cost is a struct where an adapter
may leave most fields zero; `CheckedAt` and `LastSuccess` already depend on zero
being meaningful.

`Target` and `LastError` are required to be safe to log. Credentials are resolved
before construction, so redaction is the adapter's responsibility (§14.3).

`ReasonOf` returns a boolean because the first version did not, and Agent 2 declined
to use it for a correct reason: a single return collapses "no adapter reported" into
"an adapter reported unknown", which would have swallowed the scheduler's own sentinel
chain and reported `unknown` where the snapshot store had already determined
`cardinality` or `duplicate_series`. The scheduler hand-rolled the `errors.As` walk to
keep the distinction. Rather than leave a helper in the shared contract whose
convenient form silently loses information, the helper now reports both — the same
comma-ok shape `source.Row` uses to separate an absent key from a null value.

Two maintenance couplings are now real and should move together with the telemetry
contract:

1. A consumer's accepted-reason allowlist is a second enumeration alongside
   `reason`. Adding a reason without adding it to a consumer's allowlist silently
   yields `unknown` there.
2. `reason.Cardinality` and `reason.DuplicateSeries` are deliberately **not**
   accepted from adapters. They are publication verdicts the snapshot store owns,
   and `Cardinality` also drives `metricule_cardinality_rejections_total`.

Both interfaces are optional. A source implementing neither reports unknown reasons
and no health detail, which is the current behavior and degrades visibly rather than
silently.
