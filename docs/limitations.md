# Limitations

What Metricule cannot do, or does only approximately, and why. Read this before
depending on a metric for alerting.

Several entries are deliberate design limits rather than unfinished work; those are
marked **by design** and will not change. Where a limitation has a planned fix, the
milestone is named.

> **Status.** Describes the M1 release plus M2 change-aware refresh and M3 managed
> triggers. Collection works end to end, PostgreSQL plans can use `LISTEN/NOTIFY`
> (with optional managed triggers), and SQLite `auto` plans react to
> `PRAGMA data_version`.

## Observability semantics

### A gauge is only as fresh as its last successful collection

Scraping never triggers collection (P2, AD-003), so a value can be up to one
collection interval old even when everything is healthy. During a source outage it is
as old as the staleness policy permits.

Use `metricule_collection_snapshot_age_seconds` to tell current from cached data.
Alerting on a Metricule gauge without also alerting on snapshot age will not detect a
source that stopped responding.

### Snapshot age advances on collection, not on scrape

The age gauge is set when a collection publishes, so between collections it reports
the age at publication rather than the age right now. Treat it as a lower bound.

### Counters require a persisted cumulative source value

**By design** (§8.5, AD-012). A counter must come from a monotonic cumulative value
that already exists in the source. Metricule will not derive one by counting change
notifications, because a dropped notification would silently under-report, and
notifications are explicitly not a durable event log (AD-005).

Metricule cannot verify monotonicity. A single result set shows one value, so there is
no observable difference between a genuine cumulative counter and a `COUNT(*)`
mislabelled as one. Only negative samples are rejected. Declaring
`type: counter` over a current-row count produces a metric that silently decreases and
breaks `rate()`.

A `COUNT(*)` of current rows is a gauge, even when the metric name ends in `_total`.

### Histograms and summaries are not supported

**By design for M1** (§8.5). Bucket construction from persisted aggregate data is
deferred to M5 and only if the semantics can be verified.

### Metricule exposes current state, not history

**By design** (AD-009). Rates, deltas, and aggregation over time belong in PromQL.
There is no expression engine (§12.5).

## Consistency

### Change notifications are hints, not events

**By design** (AD-005). A notification means "something may have changed"; its absence
does not prove nothing changed. PostgreSQL discards pending notifications when a
listener disconnects, and SQLite's `data_version` reports only that *something*
changed since the last read.

Every change-driven mode therefore requires a positive `fallback_interval`, and
configuration refuses to accept one without it. A lost signal leaves a metric stale
for at most that interval, not forever.

### SQLite change detection is database-wide

**By design** (§7.4). `PRAGMA data_version` cannot identify *what* changed, so any
commit invalidates every plan on that source. A busy database will refresh plans whose
tables did not change.

If several commits land between checks, only the fact that a change occurred is
observable — intermediate states are never seen.

### Cross-source atomicity is not provided

**By design** (§8.2). Publication is atomic per collection group, so a scrape never
sees a partially mapped result. It may see metric A from one source refreshed and
metric B from another not yet refreshed. Do not write alerting rules that assume two
metrics from different sources were read at the same instant.

## Collection modes

### `mode: notify` requires an adapter that can deliver notifications

`notify` is implemented for PostgreSQL through a dedicated `LISTEN` connection.
Validation rejects it on a source type that does not advertise notifications. If a
build implements less than the declared capability table, startup logs the downgrade
to reconcile-only rather than failing. Set `require_notifications: true` to make that
downgrade fatal.

SQLite cannot deliver notifications, so use `auto` there: it compares
`PRAGMA data_version` on a pinned connection and refreshes when it advances.

### `manual` and `cdc` are rejected

Reserved for future releases and refused by validation rather than silently ignored.

### `max_concurrent_collections` is a per-source bound spelled per-metric

The key can appear under a metric's `limits`, but the limit it expresses is per
source. When a source's plans disagree, the strictest value wins. Prefer setting it
only under `defaults.limits`. Candidate for tightening in M4.

### The maximum refresh delay is not configurable

§7.5 requires a bound on how long debouncing may defer a refresh under continuous
writes. It is derived as 10× the debounce interval and cannot be set directly. No
config key until an operator demonstrates a need.

## Sources

### SQLite: a read-only filesystem cannot host a live WAL database

This one contradicts a naive reading of the deployment examples, so it matters.

A WAL-mode SQLite database requires a `-wal` file and a `-shm` shared-memory index.
Readers coordinate through them, so **opening a WAL database read-only still creates
those sidecar files if they are absent.** The database file itself is never modified —
that is mechanically asserted by content hash and mtime across a collection cycle — but
the containing directory must be writable.

Consequences for deployment:

| Database mode | Mount | Works |
|---|---|---|
| WAL, sidecars already present and writable | directory writable | yes |
| WAL, read-only bind mount | `:ro` | **no** |
| Journal (non-WAL) | `:ro` | yes |

See `docs/sources/sqlite.md` for the permission matrix. Metricule never changes the
journal mode (§10.4), so it cannot convert a WAL database to make a read-only mount
work.

### SQLite: no write access, ever

**By design** (AD-007). Read-only in every release. No triggers are installed by the
SQLite adapter, and the journal mode is never altered. The file is owned by the
monitored application.

### PostgreSQL: `pool.max_idle` is a floor, not a ceiling

The adapter uses `pgx`, which bounds idle connections from *below* (`MinConns`) and has
no maximum-idle setting. `pool.max_idle` is therefore a steady-state target rather than
an enforced cap: a burst can leave more idle connections than configured until reaping
clears them, typically within a minute.

The field promises a bound the driver cannot enforce. Documented rather than fixed for
v0.1; a rename to describe the real semantics is queued for M4. `pool.max_open` is
faithful.

### MySQL: polling only, and multi-statement DSNs are refused

**By design for M5.** MySQL supports querying and polling. Binlog observation,
`notify`, and managed triggers are not implemented, so `auto` resolves to polling.
Every collection runs in `START TRANSACTION READ ONLY`; the server rejects writes even
if a query is misconfigured. A DSN enabling `multiStatements` is rejected at startup,
because one configured query must not be able to run several statements. TLS and
timeouts are configured through the DSN.

### PostgreSQL: managed triggers add write-path work

**By design** (§9.4). Opt-in managed triggers are statement-level and call
`pg_notify` once per matching statement. They never read or aggregate rows. Expect a
notification even when an `UPDATE` changed no rows, and one per statement in a batch.
On an extremely write-heavy table, polling may be cheaper than the trigger. See
[`triggers.md`](triggers.md#overhead).

### HTTP: GET only, and no streaming

**By design for M1** (§11.4). No SSE, WebSockets, webhook ingestion, GraphQL, OAuth2
token refresh, or XML. Cross-origin redirects are always refused, regardless of
`max_redirects`.

## Configuration

### Offline validation does not resolve secrets

`metricule validate` connects to nothing (§15.1) and does not read environment
variables or secret files. A configuration that validates in CI can still fail at
startup when a reference cannot be resolved.

This is intentional — validation must work where production secrets are absent — but it
means `validate` passing is not proof that `serve` will start. Use
`validate --online` where credentials are available.

### Secrets are resolved once, at startup

A rotated credential is not picked up until the process restarts. There is no hot
reload of configuration or secrets in M1.

### Cardinality limits are enforced at different layers

`max_series_per_metric` and `max_label_value_bytes` are enforced by the mapping engine
before publication; `max_total_series` is enforced by the snapshot store, since only it
sees the global published total. Both fail the affected collection atomically —
truncation is never silent (§8.7) — but a single oversized collection and a global
budget breach are reported through different telemetry.

### Presence tracking does not follow YAML merge keys

Defaulting distinguishes an explicit zero from an absent field by recording which paths
the document stated. An anchor merged into a mapping is recorded at the alias target's
path, so a value inherited through `<<:` may be treated as absent. No shipped example
uses merge keys.

## Operations

### `/readyz` does not mean "the data is current"

`/readyz` reports that configuration compiled, subsystems initialized, and every
collection has been **attempted** — not that any succeeded. A process whose every
collection has failed reads ready.

That is deliberate (§7.6's policy-defined degraded readiness): a single dead source
must not make a working exporter look broken, especially when `/metrics` is already
serving valid exposition for the sources that do respond. But it has a consequence
worth stating plainly, because getting it wrong fails silently:

**Wiring `/readyz` into a load balancer and inferring that served data is fresh is
incorrect.** Freshness is answered by `metricule_collection_success` and
`metricule_collection_snapshot_age_seconds`, both of which are exposed. Alert on
those, not on readiness.

`/metrics` always serves a valid exposition if the process is healthy; collection
failures surface through `metricule_collection_*` rather than by failing the scrape
(§13.1).

### The refresh trigger label is `trigger`, not `reason`

`metricule_collection_refresh_total` labels refresh causes as `trigger`
(`startup`, `schedule`, `notify`, `token`, `reconnect`, `reconcile`, `retry`), while
`reason` on the error and mapping families carries *failure* causes (`timeout`,
`connection`, …). They are separate vocabularies and must not be joined.

A retry is attributed to `trigger="retry"` rather than inheriting the trigger it
descended from, so a retry's causal ancestor is not recoverable from this family
alone. That is intentional: counters cannot express per-event lineage, and a retry
only ever follows a failure, so `metricule_collection_errors_total` already
identifies what was retrying and why.

### Adapter failure reasons can silently become `unknown`

Each consumer bounds the failure reasons it accepts, to keep label cardinality fixed
(§13). A reason outside a consumer's accepted set is reported as `unknown` with no
error at compile time or runtime.

If you add a reason to `internal/telemetry/reason`, check consumers' allowlists too.
`reason.Cardinality` and `reason.DuplicateSeries` are deliberately not accepted from
adapters: they are publication verdicts the snapshot store owns.

### The race detector needs a working C toolchain locally

`-race` requires cgo, while every other target builds with `CGO_ENABLED=0` (ADR 0004).
On a machine with a broken platform SDK the race build fails to *link* while everything
else succeeds, which looks like a project problem and is not. CI runs the race detector
on Linux and is authoritative.

## Not in scope

**By design** (§1.2). Distributed tracing, log aggregation, a full telemetry pipeline,
generic SQL-to-SQL synchronization, automatic discovery of application metrics,
arbitrary user-supplied scripts, a configuration UI, and any public endpoint accepting
arbitrary SQL.

Metricule is not a replacement for Prometheus, the OpenTelemetry Collector, or Grafana.
