# Metricule

**Observability without instrumentation.**

Metricule is a lightweight, source-agnostic metrics sidecar. It turns database
queries, HTTP responses, and datastore change notifications into Prometheus metrics
through declarative configuration — with no changes to the application being
monitored.

The name is a play on *metric* and *molecule*: small units of application state
assembled into meaningful observability.

> **Status: pre-release (M0).** The configuration contract, validation, CLI surface,
> JSON Schema, and telemetry families are implemented and tested. The collection
> engine and the source adapters are not. `metricule validate` works today;
> `metricule serve` exits with an explanation rather than serving an empty
> `/metrics`. See [implementation status](#implementation-status).

## Why

Plenty of services have no Prometheus endpoint and no realistic path to getting one —
legacy applications, third-party software, anything where adding an SDK means a
deployment you do not control. The state you want is usually already sitting in a
database table or a JSON status endpoint.

Metricule reads it from there.

- No application code changes, and no SDK.
- One container, one YAML file.
- Scrapes never execute queries, so a slow database cannot slow a scrape.
- Multiple sources — PostgreSQL, SQLite, HTTP/JSON — in one deployment.
- Change-driven refresh where the source supports it, polling where it does not.

## How it works

```text
PostgreSQL ─┐
SQLite     ─┼─> collection engine ─> mapping ─> snapshot ─> /metrics ─> Prometheus
HTTP/JSON  ─┘      (own schedule)                (in memory)
```

Collection and scraping are independent. Prometheus reads a snapshot that was prepared
earlier; a slow or unreachable source shows up as stale-data telemetry rather than a
failed scrape.

When a source cannot be read, Metricule serves the last known value until it ages out,
then omits the series. It never publishes a zero to fill the gap — an absent series and
a zero mean different things.

## Configuration

```yaml
apiVersion: metricule.io/v1alpha1
kind: Exporter

sources:
  appdb:
    type: postgres
    dsn:
      env: APP_DATABASE_URL

metrics:
  - name: app_jobs_pending
    help: Jobs currently awaiting processing
    type: gauge
    source: appdb
    query:
      sql: SELECT COUNT(*) AS value FROM jobs WHERE status = 'pending'
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s

  - name: app_jobs_by_status
    help: Current job count by status
    type: gauge
    source: appdb
    query:
      sql: SELECT status, COUNT(*) AS value FROM jobs GROUP BY status
    mapping:
      value: value
      labels:
        status: status
    collect:
      mode: notify
      channel: metricule_jobs_changed
      fallback_interval: 60s
```

Produces:

```text
app_jobs_pending 12
app_jobs_by_status{status="pending"} 12
app_jobs_by_status{status="running"} 4
app_jobs_by_status{status="failed"} 2
```

Unknown keys are rejected rather than ignored, and diagnostics point at the exact
configuration path:

```console
$ metricule validate --config metricule.yaml
metricule validate: configuration is invalid (1 error(s)):
  - metrics[0].collect.mode: source type "sqlite" does not support notifications; use mode "auto" to use its change-token detection
```

[`examples/reference.yaml`](examples/reference.yaml) exercises every supported field
and is validated in CI. The authoritative field list is
[`schemas/v1alpha1.schema.json`](schemas/v1alpha1.schema.json); the prose reference is
[`docs/configuration.md`](docs/configuration.md).

### Editor support

The JSON Schema ships in the repository and in each release:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/teagan42/metricule/main/schemas/v1alpha1.schema.json
```

Or locally: `metricule config schema > metricule.schema.json`

## Deployment

```yaml
services:
  metricule:
    image: ghcr.io/teagan42/metricule:latest
    user: "65532:65532"
    command: [serve, --config, /etc/metricule/config.yaml]
    volumes:
      - ./metricule.yaml:/etc/metricule/config.yaml:ro
      - application-data:/data:ro
    environment:
      APP_DATABASE_URL: ${APP_DATABASE_URL}
    ports: ["127.0.0.1:9108:9108"]
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
```

The image runs as non-root on a read-only root filesystem with all capabilities
dropped, and needs no privileged mode, Docker socket, or host namespaces. Grant
Metricule a read-only database role.

## Commands

| Command | Purpose |
|---|---|
| `metricule serve --config PATH` | Run the exporter |
| `metricule validate --config PATH` | Validate offline; connects to nothing |
| `metricule validate --config PATH --online` | Also check source reachability |
| `metricule test --config PATH --metric NAME` | Run one collection and print the result |
| `metricule config schema` | Print the configuration JSON Schema |
| `metricule version` | Print build information |

Exit codes: `0` success, `1` runtime failure, `2` usage error.

## Implementation status

| Milestone | Scope | Status |
|---|---|---|
| M0 | Module, contracts, config, schema, CLI, CI | Complete |
| M1 | Three adapters, gauge mapping, polling, exposition | Complete (v0.3.0) |
| M2 | Notifications, change tokens, debouncing, reconciliation | Complete |
| M3 | Managed PostgreSQL triggers | Complete |
| M4 | Failure injection, benchmarks, release hardening | Complete |

M1 is the minimum viable release.

## Documentation

- [Architecture](docs/architecture.md)
- [Architecture decisions](docs/adr/)
- [Production Compose examples](examples/compose/postgres/README.md)
- [Managed triggers](docs/triggers.md)
- [Performance measurements](docs/performance.md)
- [Release readiness](docs/release-readiness.md)
- [Known limitations](docs/limitations.md)
- [Implementation specification](docs/HANDOFF.md)
- [Branching and releases](docs/releasing.md)
- [Contributor rules](AGENTS.md)

## Development

```sh
make check            # format, vet, lint, unit tests
make test-race        # race detector (requires a C toolchain)
make build            # bin/metricule
make docker           # container image
```

Go 1.27 or newer. CGO is disabled by default; the SQLite driver is pure Go.

## Scope

Metricule exposes current state for Prometheus to scrape. It is not a replacement for
Prometheus, OpenTelemetry Collector, or Grafana, and it does not do tracing, log
aggregation, or historical computation — that stays in PromQL.

## License

[Apache-2.0](LICENSE)
