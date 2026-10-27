# Architecture

Metricule is a sidecar that reads application state from sources the application
already has, converts it into Prometheus metrics by configuration, and serves it from
memory.

The single structural idea: **scraping and collection are independent.** Prometheus
reads a prepared snapshot. Collection happens on its own schedule, against its own
timeouts, and a slow or dead source cannot slow down or fail a scrape (P2, AD-003).

## Runtime shape

```text
 configuration ──> compiler ──> collection plans
                                      │
         ┌────────────────────────────┼────────────────────────────┐
         │                            │                            │
   source adapter              scheduler                     change watcher
   (query execution)        (when to collect)             (invalidation signals)
         │                            │                            │
         └──────────> result set ─────┴──────── invalidate ◄───────┘
                           │
                      mapping engine
                 (convert, label, bound)
                           │
                     snapshot store
                 (atomic publication)
                           │
                      HTTP server ──────> Prometheus
```

Collection pushes into the snapshot store. Scrapes pull out of it. Nothing crosses
that boundary in the other direction.

## Components

| Package | Responsibility |
|---|---|
| `internal/config` | Parse, default, and validate configuration; reject unknown keys |
| `internal/secrets` | Resolve env and file credential references; redact everything |
| `internal/source` | The adapter contract: `Source`, `WatchableSource`, `Capabilities` |
| `internal/source/postgres` | SQL querying plus a dedicated `LISTEN` connection |
| `internal/source/sqlite` | Read-only querying plus `PRAGMA data_version` observation |
| `internal/source/httpjson` | GET requests plus RFC 9535 selector extraction |
| `internal/selector` | RFC 9535 JSONPath evaluation |
| `internal/collect` | Scheduling, coalescing, debouncing, retry, reconciliation |
| `internal/mapping` | Result sets into validated families; cardinality enforcement |
| `internal/snapshot` | Immutable published state; atomic replacement; staleness |
| `internal/exporter` | `/metrics`, `/healthz`, `/readyz` |
| `internal/telemetry` | Metricule's own metric families |
| `internal/runtime` | Compile configuration into runnable objects; resolve secrets |

`AGENTS.md` records the permitted import directions and why each matters.

## Collection modes

| Mode | Refresh trigger | M1 behavior | Reconciliation |
|---|---|---|---|
| `poll` | Fixed interval | Implemented | n/a |
| `notify` | Source notification | Implemented for PostgreSQL | Required (`fallback_interval`) |
| `auto` | Best mechanism the adapter advertises | `LISTEN` on PostgreSQL; `data_version` on SQLite | Required (`fallback_interval`) |

`manual` and `cdc` are reserved and rejected in M1.

Reconciliation is mandatory for change-driven modes, not optional tuning. A change
notification is an invalidation hint, not a durable event (AD-005): PostgreSQL drops
pending notifications when a listener disconnects, and SQLite's `data_version` reports
only that *something* changed since the last read. Without a periodic refresh, one
lost signal strands a metric at a stale value indefinitely.

## What the design refuses to do

These are deliberate and tested, not gaps:

- **Never fabricate zero.** A failed collection applies the staleness policy —
  serve the last good value until `max_stale_age`, or omit the series. It does not
  publish 0. An absent series and a zero value mean different things, and only one of
  them is honest about an unreachable database (P3, AD-010).
- **Never truncate silently.** A result exceeding a cardinality or row limit fails the
  whole collection atomically. Publishing the first 100 of 1,000 label sets would look
  like working metrics while being wrong (§8.7).
- **Never derive counters from events.** A counter requires a persisted monotonic
  value in the source. Counting notification arrivals would under-report every time one
  is dropped (AD-012). A `COUNT(*)` of current rows is a gauge even when its name ends
  in `_total` (§8.5).
- **Never mutate a source implicitly.** Trigger management is off unless configured,
  and SQLite access is read-only (AD-006, AD-007).
- **Never resolve a selector ambiguously.** A scalar selector matching several values
  is an error, not a first-match (ADR 0003).

## Concurrency model

- One scheduler goroutine per collection plan, bounded by a per-source concurrency
  limit.
- One long-lived listener goroutine per watched source and channel. Not one per
  notification: bursts coalesce into a pending refresh rather than spawning work
  (rule 10). PostgreSQL listens on its own connection; SQLite compares a pinned
  connection's `data_version`.
- Publication replaces an immutable snapshot pointer. Readers never block on a
  collection, and a scrape never observes a partially mapped result (§8.2, P8).
- Notifications arriving during a collection schedule exactly one follow-up refresh,
  so a write burst cannot queue unbounded work.

## Failure handling

| Failure | Behavior |
|---|---|
| Query timeout | Collection fails; staleness policy applies; error counter increments |
| Source unreachable | `metricule_source_up` drops to 0; `/metrics` stays responsive |
| Listener disconnect | Reconnect with backoff, re-subscribe, then reconcile |
| Cardinality overflow | Collection fails atomically; rejection counter increments |
| Mapping error | Collection fails; metric, source, and reason reported without row data |
| Configuration invalid | Startup refuses, with diagnostics anchored to config paths |

Ordering matters on reconnect: subscribe first, then reconcile. The reverse
leaves a window where a change lands after the baseline read but before the
subscription, and is therefore never seen (§9.2).

## Implementation status

| Milestone | Scope | Status |
|---|---|---|
| M0 | Module, contracts, config, schema, CLI, CI | Complete |
| M1 | Three adapters, gauge mapping, polling, exposition | Complete (v0.3.0) |
| M2 | Notifications, change tokens, debouncing, reconciliation | Complete |
| M3 | Managed PostgreSQL triggers | Not started |
| M4 | Failure injection, benchmarks, release hardening | Not started |

M1 adds the collection engine, mapping, snapshot store, exporter, and runtime wiring.
M2 adds the watcher supervisor, PostgreSQL `LISTEN/NOTIFY`, SQLite `data_version`
observation, explicit `auto` resolution, and `require_notifications`.

Known limitations, including several deliberate design limits, are recorded in
[`limitations.md`](limitations.md).
