# Writing a source adapter

An adapter turns one kind of data source into `source.ResultSet` values. Everything
downstream — scheduling, mapping, cardinality enforcement, exposition — is shared and
source-agnostic, so an adapter is responsible for connection handling, query
execution, and nothing else.

Read `AGENTS.md` for the import rules and `docs/architecture.md` for where an adapter
sits. The three shipped adapters are the reference implementations:
`internal/source/postgres`, `internal/source/sqlite`, `internal/source/httpjson`.

## The contract

```go
type Source interface {
    ID() string
    Type() string
    Capabilities() Capabilities
    Open(ctx context.Context) error
    CompileQuery(def QueryDefinition) (QuerySpec, error)
    Query(ctx context.Context, req QueryRequest) (ResultSet, error)
    Health(ctx context.Context) error
    Close() error
}
```

Implementations must be safe for concurrent use: several collectors share one source.

`WatchableSource` adds `Watch` and is optional. A source may query without observing
change.

## Registration

Capabilities are declared centrally in `internal/source/capabilities.go`, by Agent 0,
*separately* from your factory (ADR 0005). Declare what the data source can do, not
what you have implemented yet; configuration validation reads the declared table so
it can reject an unsupported collection mode before any connection is opened.

Your factory registers itself:

```go
func init() { source.Register(source.TypeSQLite, New) }
```

`Register` panics if the type has no declared capabilities. A declared type with no
registered factory reports "declared but not implemented in this build", which is a
different message from "unsupported type" — on purpose.

An adapter is only reachable if something imports it, since registration happens in
`init`. The composition root blank-imports each one, so a new adapter must be added
there or it will never load — `source.Implemented(type)` stays false and `serve`
reports the adapter as absent from the build.

> Those blank imports land with the `internal/runtime` wiring. Until then no adapter is
> linked into the binary, which is why `metricule serve` cannot yet collect.

## Settings

`source.Settings.Raw` carries your concrete settings type from
`internal/source/settings.go`. Credentials arrive **already resolved**:
`internal/runtime` is the only package that reads secret references.

Never import `internal/secrets` — redaction helpers live there for `runtime`'s use,
not yours — and never log a credential. Build a credential-free identifier once at
construction and log only that. `Diagnostics.Target` and `QuerySpec.Describe` are both
required to be safe to display; the postgres adapter derives `Target` as
`host:port/database`, which never contains the password.

## Rules that are easy to get wrong

**Error on `MaxRows`, never truncate.** §8.7 prohibits silent truncation. Returning
the first N rows of an oversized result looks like working metrics while being wrong.
Return an error and an empty `ResultSet`.

**Keep absent and null distinct.** `source.Row` is `map[string]any`. Omit the key when
a field is missing; store an explicit `nil` when the source held null. Consumers use
the two-value lookup:

```go
v, ok := row[name]
// !ok          -> missing field  -> a mapping error
// ok && v==nil -> null value     -> the configured null policy decides
```

Collapsing these loses the distinction between "the query returned no such column" and
"the column was NULL", and neither may become zero (§12.4, AD-010).

**Document adapters resolve their own selectors.** If your source returns a structured
document rather than rows, evaluate `QueryDefinition.Rows`, `Value`, and `Labels`
yourself and emit a normalized `ResultSet`. Key the resulting columns by **the selector
string itself**, so `row[mapping.Value]` resolves identically for a SQL column name and
a JSONPath expression. Renaming to canonical columns would force the mapping engine to
know which source produced the row, which P7 forbids. See ADR 0008.

**Reject non-finite numbers.** NaN and ±Inf are not valid samples. Note that
`strconv.ParseFloat("1e400", 64)` returns `+Inf` *with* an `ErrRange` error — report it
as non-finite rather than as a parse failure.

**Do not mutate the source.** Schema changes and trigger installation require explicit
authorization (P4, AD-006). SQLite access is read-only in v0.1 (AD-007).

## Classifying failures

Wrap driver errors in a type implementing `source.Classified` so the scheduler can
label telemetry without type-asserting your adapter:

```go
type queryError struct {
    err    error
    reason reason.Reason
}

func (e queryError) Error() string            { return "query: " + e.err.Error() }
func (e queryError) Unwrap() error            { return e.err }
func (e queryError) Reason() reason.Reason    { return e.reason }

var _ source.Classified = queryError{}   // compile-check it
```

Import `internal/telemetry/reason`, never `internal/telemetry` — the latter pulls in
`client_golang` and would put an exporter dependency in every adapter's transitive
graph. A test enforces this (ADR 0009).

`source.ReasonOf(err)` returns `(reason.Reason, bool)`. The boolean matters if your
caller has its own fallback chain — a single return would collapse "nobody reported a
reason" into "an adapter reported unknown", and the scheduler relies on that
distinction to fall through to its own sentinels.

> **This failure mode is silent.** `source.ReasonOf` does not validate the value, and
> each consumer bounds its own label space to keep cardinality fixed (§13). A reason
> outside a consumer's accepted set becomes `unknown` with **no error anywhere** — not
> at compile time, not at runtime. If you add a reason, check the consumer's allowlist
> in `internal/collect` as well, or your new label silently never appears.
>
> `reason.Cardinality` and `reason.DuplicateSeries` are deliberately not accepted from
> adapters: they are publication verdicts the snapshot store owns, and `Cardinality`
> also drives `metricule_cardinality_rejections_total`.

## Reporting health

Implement `source.Diagnosable` to expose health state. `internal/runtime` runs the
health loop and publishes `metricule_source_up` and
`metricule_source_reconnects_total`; adapters receive no telemetry handle, by design.

```go
var _ source.Diagnosable = (*Source)(nil)
```

`Reconnects` must be **monotonic for the life of the source** — not reset by `Close`,
nor by a later `Open`. The publisher derives a counter delta from it, so turning it
into a per-outage counter makes the exported counter move backwards. Fields you cannot
populate stay zero; `CheckedAt` and `LastSuccess` already rely on that.

## Testing

Rule 9: do not substitute mocks where real datastore behavior is under test.

- Unit-test compilation, type mapping into `ResultSet`, and error classification.
- Integration-test against the real thing, in `tests/integration/<adapter>/` behind
  the `integration` build tag, and add a job to
  `.github/workflows/integration.yaml`. Use Testcontainers for servers; real files for
  SQLite; `net/http/httptest` is a real server, not a mock.
- Cover the §20.2–§20.4 matrix for your source type.
- Assert that no credential reaches any log line or error message.
- Run `go test -race`. CI runs it on Linux, which is authoritative — see ADR 0004 for
  the local macOS SDK caveat.

Verify a behavioral claim by removing the behavior and watching the test fail. Several
of the shipped adapters' security tests were confirmed sensitive that way, and at least
one bug was found doing it.

## Checklist

- [ ] Capabilities declared in `internal/source/capabilities.go`
- [ ] Factory registered in `init`, and blank-imported at the composition root
- [ ] Settings type added to `internal/source/settings.go`
- [ ] Errors implement `source.Classified`, asserted at compile time
- [ ] `Diagnostics()` implemented, `Reconnects` monotonic
- [ ] `MaxRows` errors rather than truncates
- [ ] Absent and null kept distinct
- [ ] No credential in any log, error, or `Describe` output
- [ ] Context cancellation honored throughout
- [ ] Integration tests plus a CI job
- [ ] `make check` and `go test -race` clean
