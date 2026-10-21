# ADR 0001: Settled baseline decisions

Status: Accepted
Date: 2026-10-21

## Context

`docs/HANDOFF.md` §24 records fifteen decisions as settled before implementation
began. They are reproduced here so the decision log is complete without reading the
whole specification, and so later records can supersede individual items.

These are not open questions. §24 states they stand "unless a demonstrable
correctness issue requires revision."

## Decision

| ID | Decision | Enforced by |
|---|---|---|
| AD-001 | Go is the implementation language | `go.mod` |
| AD-002 | YAML is the user-facing configuration format | `internal/config` |
| AD-003 | Prometheus scraping and source collection are independent | P2; `internal/snapshot` |
| AD-004 | Published snapshots are immutable during exposition | `snapshot.Store` contract |
| AD-005 | Change notifications are invalidation signals, not durable events | `source.ChangeEvent`; mandatory reconciliation |
| AD-006 | PostgreSQL trigger management is opt-in | `config.Triggers.Manage` defaults false |
| AD-007 | SQLite collection is read-only in v0.1 | `readonly` forced true in defaults |
| AD-008 | Initial support targets PostgreSQL, SQLite, and HTTP/JSON | `source.DeclareCapabilities` |
| AD-009 | PromQL remains responsible for historical computation | No expression engine (§12.5) |
| AD-010 | Missing or invalid samples are not silently coerced to zero | Null policy defaults to `fail`; P3 |
| AD-011 | Label cardinality and source resource consumption are bounded | `config.Limits` |
| AD-012 | Event-derived counters require durable semantics first | Counters need a persisted cumulative value (§8.5) |
| AD-013 | No dashboard or web configuration UI | Not built |
| AD-014 | OCI container distribution is first-class | `Dockerfile`, release workflow |
| AD-015 | The binary also works outside containers | CI builds darwin and linux binaries |

## Consequences

Several of these are load-bearing in ways worth restating:

- AD-005 is why every change-driven collection mode requires a positive
  `fallback_interval`. Configuration validation refuses to accept a notify-only
  metric, because a lost notification would otherwise leave a metric stale forever.
- AD-010 and P3 together rule out the most common exporter shortcut: returning 0 when
  a source is unreachable. The staleness policy decides between serving the last good
  value and omitting the series, and neither path invents a number.
- AD-012 rules out counters derived from notification arrival. A notification is not
  an event log, so counting notifications would under-report whenever one is dropped.

AD-007 costs real functionality: SQLite change detection is limited to
database-wide `PRAGMA data_version` observation, since installing triggers would
require writing to an application-owned database.
