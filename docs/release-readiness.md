# Release readiness

This is the evidence map for the M0–M4 acceptance criteria. It records what was
verified, how, and what remains a documented limitation. It is not a certification:
re-run the gates on the release commit and read
[`limitations.md`](limitations.md) before depending on a metric for alerting.

## Verification gates

| Gate | Command | Expected |
|---|---|---|
| Format, vet, lint | `make check` | no diagnostics |
| Unit tests | `make test` | all packages pass |
| Race detector | `make test-race` | all packages pass |
| Integration | `make test-integration` | PostgreSQL and SQLite jobs pass (Docker required) |
| Examples | `make validate-examples` | every documented configuration validates |
| Compose | `make validate-compose` | every stack resolves |
| Performance | `make bench`, `make bench-integration`, `make bench-idle` | numbers recorded in [`performance.md`](performance.md) |
| Vulnerability scan | CI `govulncheck ./...` | no known vulnerable dependency in a reachable path |
| Release | `Release` workflow | four binaries, checksums, multi-arch image, SBOM, schema |

CI runs the first six (excluding Docker-only benchmarks) plus the vulnerability scan
on every pull request. Benchmarks are manual by design: they are a baseline, not a
pass/fail gate on shared runners.

## Milestones

| Milestone | Acceptance | Evidence |
|---|---|---|
| M0 | Contracts, configuration, schema, CLI, telemetry | `internal/config`, `cmd/metricule`, `schemas/`; CLI and schema parity tests |
| M1 | Three adapters, gauge mapping, polling, exposition | SQLite/PostgreSQL/HTTP integration tests; exporter and scheduler tests; `GET /metrics` performs no upstream I/O |
| M2 | Notifications, change tokens, debouncing, reconciliation | `#23` supervisor, `#25` SQLite `data_version`, `#26` PostgreSQL `LISTEN`; outage and notification tests in `tests/integration` |
| M3 | Opt-in managed PostgreSQL triggers | `#29`; apply/status/remove, idempotency, foreign-object refusal, runtime startup/cleanup tests |
| M4 | Failure injection, security, telemetry, packaging, benchmarks, docs | outage and slow-collection tests (`#32`), benchmark harness (`#31`), Compose stacks (`#33`), secrets redaction tests, [`performance.md`](performance.md) |

## Release acceptance checklist

Adapted from the implementation handoff, with the artifact or test that satisfies it.

| Criterion | Status | Evidence |
|---|---|---|
| `validate` CLI | met | `cmd/metricule`, offline and `--online` modes, parity tests |
| `test` CLI | met | `cmd/metricule`, server and adapter tests |
| Graceful shutdown | met | runtime tests; SIGTERM in the startup benchmark |
| Non-root container | met | `Dockerfile` (`USER 65532`), Compose stacks run read-only with dropped capabilities |
| Docker Compose examples | met | [`examples/compose/postgres`](../examples/compose/postgres/README.md), [`examples/compose/sqlite`](../examples/compose/sqlite/README.md), CI validation |
| linux/amd64 and linux/arm64 images | met | multi-arch release job; image attestation |
| GitHub Actions CI | met | `.github/workflows/ci.yaml`, integration, release, PR-title |
| GHCR publishing | met | `Publish multi-architecture image` |
| Unit tests | met | `make check` |
| Integration tests | met | `make test-integration` |
| Race detector clean | met | `make test-race` |
| Failure injection | met | source outage, slow collection, restarts, cancellation, timeout, lock contention, permission denial, file replacement |
| Security scanning | met | `govulncheck`; read-only SQL enforcement; secret redaction tests; redirect and size limits |
| SBOM | met | `metricule.spdx.json` attached to each release |
| Performance benchmarks | met | [`performance.md`](performance.md) |
| Full user documentation | met | `configuration.md`, adapter notes, `triggers.md`, `performance.md`, Compose READMEs |

## Reproducibility

- Every build uses the committed `go.mod`/`go.sum` with `-trimpath` and linker-injected
  version metadata.
- Release binaries are built with `CGO_ENABLED=0` on pinned Go and Alpine images; the
  runtime image is distroless and runs as uid 65532.
- Images are published for `linux/amd64` and `linux/arm64` with a digest and build
  provenance attestation.
- Byte-identical rebuilds are not promised: base images are tag-pinned, not
  digest-pinned, and Go embeds build metadata. Reproduce the source and the artifact
  set, not necessarily the same bytes.

## Security posture

- Collection is read-only by enforcement, not by SQL inspection: PostgreSQL sessions
  force `default_transaction_read_only` and wrap each query in a read-only
  transaction; SQLite opens with `mode=ro` and `_query_only=1`.
- Trigger provisioning is opt-in, uses a separate DSN, and only touches objects with
  its ownership marker.
- Credentials resolve in `internal/runtime` only. Adapters, logs, errors, diagnostics,
  and configuration dumps never render a secret; `secrets.RedactDSN` covers URL,
  query, and key-value DSNs.
- Paths, methods, redirect origins, response sizes, rows, series, and concurrency are
  bounded by configuration.

## Accepted limitations

These are documented in [`limitations.md`](limitations.md); none is a release blocker:

- No summaries, event-derived counters, expression evaluation, or non-GET HTTP
  methods. Histograms are supported only from persisted cumulative buckets.
- `manual` and `cdc` modes are reserved.
- `PRAGMA data_version` is database-wide.
- PostgreSQL `pool.max_idle` is a steady-state target, not a hard cap.
- `max_concurrent_collections` is a per-source bound spelled per metric; the strictest
  value wins.
- Managed triggers are statement-level DML only; the managed schema is not removed.
- Scrape cost grows linearly with series count; 100k series allocates ~81 MB per
  scrape (descriptor construction), which is a recorded optimization opportunity.

## Before tagging a release

1. Rebase the release pull request on green `main`.
2. Run `make check`, `make test-race`, and `make test-integration` on the release
   commit.
3. Run `make bench` and update [`performance.md`](performance.md) if the result moves
   materially.
4. Confirm the previous release's assets and images are complete.
5. Merge the release pull request and verify the `Release` workflow: binaries,
   checksums, image, SBOM, schema.
