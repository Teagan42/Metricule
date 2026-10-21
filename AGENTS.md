# AGENTS.md

Binding coordination rules for all agents working on Metricule.

Read this file and `docs/HANDOFF.md` before modifying code. `docs/HANDOFF.md` is the
implementation contract; this file is the subset you must not violate.

## Non-negotiable rules

1. Read the specification and this file before modifying code.
2. Do not change shared interfaces without coordination with Agent 0.
3. Do not silently narrow supported functionality.
4. Do not stub declared capabilities and report them implemented.
5. Do not introduce direct dependencies between unrelated adapters.
6. Do not put business logic inside the HTTP handler.
7. Do not create metric families dynamically from unvalidated arbitrary input.
8. Do not add application-specific behavior to shared components.
9. Do not replace real integration tests with mocks when actual datastore behavior is
   under test.
10. Do not add unbounded queues or goroutine-per-notification designs.
11. Do not provision database objects without explicit configuration.
12. Do not rely on webhook or database notifications as a durable event history.
13. Do not expose credentials or sensitive records in logs.
14. Do not merge features without tests and documentation.
15. Do not report a milestone complete unless its acceptance criteria have passed.

Priority order: **correctness, safety, operability, simplicity, performance,
extensibility.**

## Engineering principles

| ID | Principle |
|---|---|
| P1 | Declarative first — behavior comes from config, not source-specific code |
| P2 | Scrape isolation — `GET /metrics` runs no SQL, no upstream HTTP, and never waits on collection |
| P3 | Fail visibly — failed collectors update health telemetry; never fabricate zeros |
| P4 | No implicit mutation — source mutation requires explicit authorization |
| P5 | Bounded resources — timeouts, concurrency caps, result caps, cardinality caps |
| P6 | Correctness over apparent freshness — distinguish current, cached, invalid, unavailable |
| P7 | Adapter independence — scheduler and mapper hold no source-specific logic |
| P8 | Safe concurrency — concurrent scrape/refresh/notify never exposes partial results |
| P9 | Operability — significant operations expose logs and exporter-owned metrics |
| P10 | No unnecessary infrastructure — no broker or control plane for Metricule itself |

## Ownership

Do not modify another agent's packages without coordination. Interface changes always
route through Agent 0.

| Agent | Scope | Owns |
|---|---|---|
| 0 | Technical lead | Interfaces, config contract, ADRs, integration |
| 1 | Configuration & CLI | `internal/config`, `cmd/metricule`, `schemas/` |
| 2 | Scheduler & snapshots | `internal/collect`, `internal/snapshot` |
| 3 | Mapping & exporter | `internal/mapping`, `internal/exporter` |
| 4 | PostgreSQL adapter | `internal/source/postgres` |
| 5 | SQLite adapter | `internal/source/sqlite` |
| 6 | HTTP/JSON adapter | `internal/source/httpjson` |
| 7 | Trigger management | `internal/triggers` |
| 8 | CI, packaging, docs | `Dockerfile`, `.github/`, `examples/`, `docs/` |
| 9 | QA / adversarial review | Cross-cutting tests |

`internal/source` (root package), `internal/telemetry`, and `internal/secrets` are
shared contracts owned by Agent 0.

## Package dependency direction

Imports flow one way. Adding an edge not listed here requires Agent 0 review.

```text
config  <- source, collect, mapping, exporter, runtime
secrets <- config
source  <- source/{postgres,sqlite,httpjson}, collect, triggers
snapshot <- mapping, exporter, collect
collect <- runtime
mapping <- collect
telemetry <- everything
```

Adapters MUST NOT import each other. `internal/collect` and `internal/mapping` MUST NOT
import any `internal/source/*` subpackage.

## Pull request requirements

Every PR must state:

- Problem addressed.
- Architectural impact.
- Implementation summary.
- Test coverage.
- Configuration changes.
- Compatibility considerations.
- Known limitations.

Produce reproducible evidence for behavioral claims. A claim that a test passes must be
accompanied by the command and its output.

## Definition of Done

- The implementation exists.
- Compilation passes.
- Relevant tests pass.
- Race tests pass where concurrency is involved.
- Config behavior is validated.
- Documentation is updated.
- Known limitations are stated.
- No debug artifacts or placeholder implementations remain.

## Code conventions

- Comments explain why, not what. No control-flow narration. Inline comments stay under
  15 words; doc comments under 30.
- Doc comments open with an imperative verb.
- Wrap errors with `%w` and enough context to identify metric, source, and operation.
- Never log secret-bearing values. Route all credential access through
  `internal/secrets`.
- Use `log/slog` for structured logging. No `fmt.Println` in library code.
- Every exported type in a shared contract package needs a doc comment.

## Verification

```sh
make check    # fmt, vet, lint, unit tests
make test-race
make test-integration   # requires Docker
```
