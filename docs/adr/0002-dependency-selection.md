# ADR 0002: Dependency selection and pinning

Status: Accepted
Date: 2026-10-21

## Context

§4.2 names preferred libraries and requires the final choices be recorded. §4.3 adds
that no dependency may exist purely for convenience where the standard library
suffices, that versions must be pinned, and that every major choice be documented.

Agents work in parallel, so an unpinned or undecided dependency becomes a merge
conflict in `go.mod` and, worse, divergent behavior between packages.

## Decision

| Concern | Choice | Version |
|---|---|---|
| Prometheus metrics | `github.com/prometheus/client_golang` | v1.25.0 |
| PostgreSQL | `github.com/jackc/pgx/v5` | v5.11.0 |
| SQLite | `modernc.org/sqlite` | v1.61.0 |
| YAML | `go.yaml.in/yaml/v3` | v3.0.5 |
| JSON Schema validation | `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 |
| JSON selectors | `github.com/theory/jsonpath` | v0.12.2 |

Used from the standard library instead of a dependency:

- **CLI parsing**: `flag`. See ADR 0006.
- **Structured logging**: `log/slog`.
- **HTTP client and server**: `net/http`.
- **Embedding the schema**: `embed`.

`github.com/tidwall/gjson` was evaluated and rejected. See ADR 0003.

## Consequences

`modernc.org/sqlite` is a pure-Go translation of SQLite rather than a binding, which
is what makes ADR 0004 possible. The cost is a large transitive tree
(`modernc.org/libc`, `memory`, `mathutil`) and a reimplementation that can lag
upstream SQLite. The alternative, `mattn/go-sqlite3`, requires cgo and would force a
glibc-linked image.

`pgx/v5` is used directly rather than through `database/sql`, because §9.2 requires a
dedicated long-lived connection for `LISTEN` that is never returned to a pool.
`database/sql` offers no such guarantee.

Pinning exact versions means dependency updates are deliberate commits that CI
validates, rather than silent drift. `go mod tidy` is enforced in CI.
