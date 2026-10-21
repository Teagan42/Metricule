# Metricule
## Agent Implementation Handoff & Technical Specification

**Version:** 0.1  
**Status:** Approved design baseline for implementation  
**Implementation Language:** Go  
**License:** Apache-2.0  
**Primary Deployment:** OCI/Docker container  
**Target Platforms:** linux/amd64, linux/arm64  
**Tagline:** *Observability without instrumentation.*

---

# 1. Executive Summary

Metricule is a self-contained, declarative Prometheus exporter that derives application metrics from existing data sources, without requiring changes to application code.

It supports scheduled polling and opportunistic change-driven collection, exposing a consistent and inexpensive `/metrics` endpoint backed by an in-memory metric snapshot.

A user defines:

1. Data sources, including connections and authentication.
2. Metrics and their types.
3. Queries, HTTP endpoints, and result selectors.
4. Label extraction and numeric transformations.
5. Collection schedules and change-detection strategies.
6. Refresh, failure, cardinality, and staleness policies.

The system handles connection management, query execution, refresh scheduling, data conversion, change observation, caching, and Prometheus exposition.

**Core principle: Prometheus scrapes the exporter. The exporter collects from application data sources independently.**

Metricule MUST NOT require modifications to the monitored application.

Optional datastore-side triggers MAY be provisioned when explicitly authorized.

## 1.1 Product goals

- Make useful application metrics available through configuration alone.
- Work with existing and legacy services that lack native Prometheus integration.
- Support multiple datastore and API sources in a single deployment.
- Avoid executing expensive queries on every Prometheus scrape.
- Detect source changes and refresh affected metrics efficiently.
- Minimize privileges and database modification.
- Provide deterministic, validated metric output.
- Be suitable for unattended production deployment.
- Remain small enough to run as a Docker sidecar.
- Offer an extensible adapter interface for additional source types.
- Provide actionable diagnostics when collection fails.

## 1.2 Non-goals for v0.1

- Distributed tracing.
- Log aggregation.
- A full telemetry pipeline.
- Replacing Prometheus, OpenTelemetry Collector, or Grafana.
- Generic SQL-to-SQL synchronization.
- Automatic discovery of all application metrics.
- Inferring undocumented application semantics.
- Durable, event-accurate counters from arbitrary transient state changes.
- Arbitrary user-provided executable scripts.
- Arbitrary SQL execution through exposed HTTP endpoints.
- A custom UI.
- Reimplementing PromQL.
- Shipping historical samples directly to Prometheus.

## 1.3 Success criteria

A user with a running PostgreSQL, SQLite, or HTTP-backed application MUST be able to:

1. Supply one YAML configuration.
2. Start one Metricule container.
3. Receive meaningful Prometheus metrics.
4. Observe successful and failed refreshes.
5. Configure collection without changing application code.
6. Enable supported change-driven refreshes.
7. Validate the configuration without starting the daemon.

No application-side SDK integration should be required.

---

# 2. Mandatory Engineering Principles

All implementing agents MUST adhere to these principles.

### P1. Declarative first

Behavior is described by configuration rather than application-specific code.

### P2. Scrape isolation

`GET /metrics` MUST NOT execute SQL queries, make upstream HTTP requests, or wait on collection operations.

### P3. Fail visibly

A failed collector MUST update its error and health telemetry. Failures MUST NOT silently return fabricated zeros.

### P4. No implicit mutation

Database schema modifications, trigger installation, and other source mutations MUST require explicit authorization.

### P5. Bounded resource consumption

All collections require timeouts, concurrency limits, result limits, and metric cardinality limits.

### P6. Correctness over apparent freshness

Metricule MUST distinguish current state, cached state, invalid data, and unavailable data.

### P7. Adapter independence

The collection scheduler and metric mapper MUST NOT contain source-specific implementation logic.

### P8. Safe concurrency

Concurrent scrapes, refreshes, and change notifications MUST NOT expose partially updated collection results.

### P9. Operability

All significant internal operations MUST expose diagnostics through logs and exporter-owned Prometheus metrics.

### P10. No unnecessary infrastructure

The MVP MUST NOT require Kafka, Redis, PostgreSQL, an additional control-plane service, or a message broker for Metricule itself.

---

# 3. High-Level Architecture

```text
                     +----------------------+
                     |   YAML Configuration |
                     |     + Validation     |
                     +-----------+----------+
                                 |
                                 v
                     +----------------------+
                     |   Runtime Compiler   |
                     | Sources / Metrics /  |
                     |   Collection Plans   |
                     +-----------+----------+
                                 |
                                 v
+------------------+   +------------------------+
| PostgreSQL       |-->|                        |
| - SQL polling    |   |                        |
| - LISTEN/NOTIFY  |   |   Collection Engine    |
+------------------+   |                        |
                       | - Scheduling           |
+------------------+   | - Deduplication        |
| SQLite           |-->| - Debouncing           |
| - SQL polling    |   | - Timeouts             |
| - data_version   |   | - Retry/backoff        |
+------------------+   | - Reconciliation       |
                       | - Dependency tracking  |
+------------------+   |                        |
| HTTP/JSON        |-->|                        |
| - REST requests  |   +-----------+------------+
| - JSON selectors |               |
+------------------+               v
                       +------------------------+
                       |  Mapping/Validation    |
                       |                        |
                       | - Numeric conversion   |
                       | - Labels               |
                       | - Type enforcement     |
                       | - Cardinality checks   |
                       +-----------+------------+
                                   |
                                   v
                       +------------------------+
                       | Metric Snapshot Store  |
                       |                        |
                       | - Atomic publication   |
                       | - Source freshness     |
                       | - Series lifecycle     |
                       +-----------+------------+
                                   |
                                   v
                       +------------------------+
                       | HTTP Server            |
                       |                        |
                       | GET /metrics           |
                       | GET /healthz           |
                       | GET /readyz            |
                       +-----------+------------+
                                   |
                                   v
                              Prometheus
```

## 3.1 Major components

### Configuration subsystem
Parses, validates, resolves secrets, and compiles declarative configuration into executable collection plans.

### Source manager
Maintains shared source instances, connection pools, authentication material, and source lifecycle.

### Adapter layer
Implements source-specific querying and optional change observation.

### Scheduler
Coordinates periodic collection, notification-driven refresh, backoff, coalescing, and source concurrency.

### Mapping engine
Converts normalized source results into validated Prometheus metric families and samples.

### Snapshot store
Stores successfully collected metric families for lock-efficient exposition.

### HTTP server
Publishes Prometheus metrics and health endpoints.

### Trigger manager
Optionally provisions, inspects, and removes managed PostgreSQL change-notification triggers.

### Telemetry subsystem
Instruments Metricule itself without depending on external collectors.

---

# 4. Technology Decisions

## 4.1 Language

Go, using the latest stable supported Go release selected and pinned at implementation start.

Required qualities:
- Native concurrency.
- Small OCI deployment.
- Efficient database and HTTP connectivity.
- Strong Prometheus client ecosystem.
- Cross-platform build support.
- Simple static distribution.

## 4.2 Preferred libraries

Evaluate the following, pin versions, and record final choices in an ADR:

| Concern | Preferred option |
|---|---|
| Prometheus metrics | prometheus/client_golang |
| PostgreSQL | jackc/pgx/v5 |
| SQLite | modernc.org/sqlite, if operationally suitable |
| HTTP | net/http |
| YAML | go.yaml.in/yaml/v3 or maintained equivalent |
| Structured logging | log/slog |
| JSON selectors | Maintained JSONPath or GJSON-compatible library |
| Testing | Go testing, Testcontainers |
| Schema validation | JSON Schema Draft 2020-12 |
| Container packaging | Multi-stage Dockerfile |
| CI | GitHub Actions |

Use established libraries. Do not implement proprietary SQL drivers, connection pools, JSONPath parsers, or Prometheus encoders.

## 4.3 Dependency policy

- No dependency purely for convenience if Go's standard library is sufficient.
- No CGO requirement in the default release unless a demonstrated need justifies it.
- Dependencies MUST be version-pinned.
- Security scanning and license checks MUST run in CI.
- Every major dependency decision MUST be documented.

---

# 5. Configuration Contract

The configuration format is a versioned public interface.

Unknown keys MUST fail validation rather than being silently ignored.

## 5.1 Complete example

```yaml
apiVersion: metricule.io/v1alpha1
kind: Exporter

metadata:
  name: example-app
  description: Application metrics exporter

server:
  listen: "0.0.0.0:9108"
  metrics_path: /metrics
  health_path: /healthz
  readiness_path: /readyz

defaults:
  collection:
    interval: 30s
    timeout: 10s
    reconcile_interval: 5m
    debounce: 250ms
  limits:
    max_series_per_metric: 1000
    max_rows_per_query: 10000
    max_concurrent_collections: 4

sources:
  appdb:
    type: postgres
    dsn:
      env: APP_DATABASE_URL
    pool:
      max_open: 4
      max_idle: 2
    query_timeout: 10s

  localdb:
    type: sqlite
    path: /data/app.db
    readonly: true
    busy_timeout: 3s
    change_check_interval: 2s

  appapi:
    type: http
    base_url: http://app:8080
    timeout: 5s
    authentication:
      type: bearer
      token:
        file: /run/secrets/app_api_token
    allowed_paths:
      - /api/status
      - /api/health

metrics:
  - name: app_jobs_pending
    help: Jobs currently awaiting processing
    type: gauge
    source: appdb

    query:
      sql: |
        SELECT COUNT(*) AS value
        FROM jobs
        WHERE status = 'pending'

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
      sql: |
        SELECT status, COUNT(*) AS value
        FROM jobs
        GROUP BY status

    mapping:
      value: value
      labels:
        status: status

    collect:
      mode: notify
      channel: metricule_jobs_changed
      fallback_interval: 60s
      debounce: 250ms

  - name: app_users_total
    help: Current user population
    type: gauge
    source: localdb

    query:
      sql: SELECT COUNT(*) AS value FROM users

    mapping:
      value: value

    collect:
      mode: auto
      watch:
        tables:
          - users
      fallback_interval: 60s

  - name: app_active_sessions
    help: Currently active application sessions
    type: gauge
    source: appapi

    request:
      method: GET
      path: /api/status

    mapping:
      value: "$.sessions.active"

    collect:
      mode: poll
      interval: 15s

  - name: app_service_state
    help: Current service operating state
    type: state_set
    source: appapi

    request:
      method: GET
      path: /api/status

    mapping:
      value: "$.state"
      states:
        - starting
        - running
        - degraded
        - stopped

    collect:
      mode: poll
      interval: 15s
```

The `state_set` type is a Metricule mapping abstraction, not a separate Prometheus wire type. It emits a gauge series with a bounded `state` label, using value 1 for the active state and 0 for the remaining configured states.

The above example defines the desired configuration contract. Agents MUST implement or explicitly mark unsupported fields through phase-appropriate validation. They MUST NOT silently accept nonfunctional configuration.

## 5.2 Configuration versioning

Required root properties:

- `apiVersion`
- `kind`
- `sources`
- `metrics`

Use strict semantic validation.

Configuration schema MUST be distributed in the repository and release artifacts.

## 5.3 Secret references

Support:

```yaml
dsn:
  env: DATABASE_URL
```

and:

```yaml
dsn:
  file: /run/secrets/database_url
```

Literal values MAY be supported for development, but secret-bearing values MUST never be logged.

Secrets MUST NOT be exposed through metrics, validation diagnostics, HTTP responses, or error messages.

## 5.4 Environment expansion

Prefer explicit secret/value reference objects over unrestricted `${VARIABLE}` expansion.

Unresolved required references MUST fail validation at startup.

## 5.5 Validation

Validation MUST detect:

- Duplicate metric names.
- Missing sources.
- Unsupported source types.
- Unsupported mapping types.
- Invalid Prometheus names and labels.
- Reserved naming collisions.
- Incorrect collection configuration.
- Missing authentication references.
- Invalid selectors.
- Invalid duration formats.
- Invalid cardinality limits.
- Duplicate or conflicting output families.
- Unsupported features for an adapter.

SQL syntax and actual result schema validation require access to a source and therefore belong to an optional online validation mode.

Provide both offline and online validation.

---

# 6. Core Go Interfaces

The agent responsible for core architecture MUST establish interfaces before adapter implementation begins.

The following are conceptual contracts. Agents may refine types while preserving responsibilities.

```go
type Source interface {
    ID() string
    Open(ctx context.Context) error
    Query(
        ctx context.Context,
        request QueryRequest,
    ) (ResultSet, error)
    Health(ctx context.Context) error
    Close() error
}

type WatchableSource interface {
    Source

    Watch(
        ctx context.Context,
        spec WatchSpec,
        events chan<- ChangeEvent,
    ) error
}

type ChangeEvent struct {
    SourceID string
    Scope    string
    Kind     string
    At       time.Time
}

type ResultSet struct {
    Columns []Column
    Rows    []Row
}

type Row map[string]any

type Collector interface {
    ID() string
    Collect(
        ctx context.Context,
    ) (CollectionResult, error)
}
```

## 6.1 Important design constraints

- `Watch` is optional.
- A source can support querying without change observation.
- The scheduler MUST NOT inspect concrete adapter types.
- Change events represent invalidation signals, not necessarily durable business events.
- Collection results MUST include timestamps, diagnostics, and source identifiers.
- Collection state MUST be independently observable.
- Adapter shutdown MUST terminate listeners and release resources.
- A source MUST NOT assume it is the only connection to a datastore.

## 6.2 Capability advertisement

Provide an explicit adapter capability descriptor.

```go
type Capabilities struct {
    Query           bool
    Poll            bool
    Notifications   bool
    ChangeTokens    bool
    CDC             bool
    TriggerManagement bool
}
```

Unsupported requested capabilities MUST produce actionable validation errors.

---

# 7. Collection Engine

The collection engine is the central subsystem.

It MUST operate independently of the HTTP exporter.

## 7.1 Supported modes

### `poll`
Refresh on a configured interval.

### `notify`
Refresh on supported source notifications, plus mandatory periodic reconciliation.

### `auto`
Select the best available collection mechanism using adapter capabilities and configured watch scope.

### `manual`
Reserved for a future authenticated administration/control interface. NOT required in v0.1.

### `cdc`
Reserved for a future durable event processing layer. NOT required in v0.1.

## 7.2 Collection lifecycle

```text
Source available
      |
      v
Scheduled / notified
      |
      v
Collection requested
      |
      v
Already running?
   |           |
  YES          NO
   |           |
Coalesce       v
request    Acquire capacity
               |
               v
          Execute query
               |
               v
          Map result
               |
               v
          Validate sample set
               |
          +----+----+
          |         |
        Valid     Invalid
          |         |
          v         v
     Publish      Record error
     snapshot     Preserve/drop per policy
          |
          v
      Update telemetry
```

## 7.3 Scheduling requirements

- Each metric has its own collection schedule.
- Metrics sharing an identical source query MAY share query execution through explicitly compiled collection groups.
- A notification MUST invalidate all dependent collection groups.
- Concurrent invalidations MUST coalesce.
- Refresh execution MUST be bounded.
- Per-source concurrency limits MUST apply.
- Collection operations MUST respect context cancellation.
- Backpressure MUST not create unbounded pending work.
- Interval schedules SHOULD include optional jitter.
- Failed collections MUST use bounded retry/backoff.
- Successful events MUST reset retry state.
- Source reconnect MUST initiate reconciliation.

## 7.4 Dependency tracking

Build an internal graph:

```text
source
  -> watch scope
      -> collection plan
          -> metric families
```

Only affected plans should refresh when the change scope can be identified reliably.

When source change scope is unknown, all plans depending on that source MAY be invalidated.

For SQLite `data_version`, assume database-wide invalidation.

## 7.5 Debouncing

Multiple change notifications in a small interval SHOULD produce one refresh.

Required:
- Leading/trailing semantics documented.
- Maximum refresh delay.
- No unbounded starvation under continuous writes.
- Coalescing while refresh is running.
- Additional refresh if writes occur during a refresh.
- Periodic reconciliation even when notifications are frequent.

## 7.6 Initial collection

At startup, all active plans SHOULD run an initial collection.

Readiness MUST distinguish:

- Configuration loaded.
- Sources initialized.
- Initial collection successfully completed, or policy-defined degraded readiness.

No metric should appear as zero merely because its first collection has not happened.

---

# 8. Snapshot Store & Prometheus Exposition

## 8.1 Core requirement

The HTTP scrape handler MUST read previously prepared metric snapshots.

It MUST NOT invoke the source adapters.

## 8.2 Atomicity

Publication MUST be atomic per collection group.

A scrape MUST never observe a partially mapped result.

Cross-source global atomicity is not required.

Immutable snapshots with atomic pointer replacement or an equivalent synchronized design are recommended.

## 8.3 Series lifecycle

On successful collection:

- Replace the previous series set belonging to that collection group.
- Remove series absent from the new successful result.
- Preserve declared metric metadata.
- Reject duplicate labelsets within a metric family.
- Reject invalid numeric samples.
- Apply cardinality limits before publication.

On failed collection:

- Record failure telemetry.
- Apply configured staleness policy.
- Never replace the last result with fabricated zero samples.

## 8.4 Failure and staleness policies

Support:

```yaml
failure:
  mode: retain
  max_stale_age: 5m
```

Modes:

- `retain`: expose the last successful snapshot until the configured age limit.
- `drop`: immediately omit affected samples after failure.

Default: `retain` with a bounded age limit.

After expiry, omit the stale samples. Do not invent timestamps or rely on an internal zero value to imply unavailable state.

Expose the age and last refresh outcome through exporter-owned telemetry.

## 8.5 Native Prometheus types

v0.1 MUST support:

- Gauge
- Counter, when sourced from a persisted monotonic cumulative value
- Bounded state-set mapping represented as gauges

Historically derived counters, event-driven counters, histograms, and summaries are deferred unless their semantics can be verified.

A SQL `COUNT(*)` of current rows MUST normally map to a gauge even if its metric name ends with `_total`.

A counter MUST have monotonic cumulative semantics, aside from genuine resets.

## 8.6 Metric naming

Use standard Prometheus conventions:

- Lowercase snake_case by default.
- Meaningful namespace prefixes.
- Base units.
- `_total` for counters.
- `_seconds`, `_bytes`, etc., for units.
- Avoid reserved suffix conflicts.
- No variable data in metric names.
- Variable dimensions belong in labels.

## 8.7 Cardinality policy

Each metric MUST specify or inherit a series cap.

Required safeguards:

- Maximum rows per query.
- Maximum distinct labelsets.
- Maximum length for label values.
- Maximum total configured metrics.
- Global upper bound for published samples.
- Rejection of unsafe cardinality overflow.
- Clear diagnostic telemetry.

On cardinality overflow, the affected collection MUST fail atomically and preserve or drop its previous snapshot according to failure policy.

Silently truncating results is prohibited.

---

# 9. PostgreSQL Adapter

## 9.1 Requirements

Support:

- Connection URL / DSN.
- TLS configuration.
- Connection pooling.
- Read-only SQL querying.
- Query timeout.
- Context cancellation.
- Transaction isolation appropriate for consistent aggregate reads.
- `LISTEN/NOTIFY`.
- Automatic reconnect.
- Backoff.
- Notification-driven invalidation.
- Explicit health diagnostics.

## 9.2 Notification listener

Use a dedicated long-lived PostgreSQL connection for `LISTEN`.

Do not use a pooled connection that may be released between waits.

Notifications are advisory invalidation messages.

Never assume that notification delivery is complete or durable.

On listener reconnection:
1. Establish connection.
2. Install `LISTEN` registrations.
3. Perform reconciliation.
4. Resume normal notification-driven refresh.

Ensure ordering prevents an unnoticed gap between subscription and baseline refresh.

## 9.3 Trigger management

Optional capability:

```yaml
sources:
  appdb:
    type: postgres
    dsn:
      env: DATABASE_URL

    triggers:
      manage: true
      schema: metricule
      cleanup_on_shutdown: false

metrics:
  - name: app_jobs_pending
    type: gauge
    source: appdb

    query:
      sql: SELECT COUNT(*) AS value FROM jobs

    mapping:
      value: value

    collect:
      mode: notify
      channel: metricule_jobs_changed
      watch:
        tables:
          - public.jobs
        events:
          - insert
          - update
          - delete
```

### Trigger safety

- Trigger management MUST be disabled by default.
- Generate schema-qualified SQL.
- Escape SQL identifiers safely.
- Generate stable names.
- Detect existing managed objects.
- Never overwrite an unrelated trigger or function.
- Provisioning MUST be idempotent.
- Require privileges explicitly.
- Maintain a clear ownership marker or registry.
- Provide dry-run SQL output.
- Never remove user-created objects.
- Never perform migrations from the ordinary metric collection connection.
- Avoid one trigger per metric when a shared table-level notification mechanism suffices.
- Prefer statement-level triggers for cache invalidation.

### Lifecycle policy

Do NOT automatically delete managed triggers on normal shutdown.

A restarted sidecar should not create unnecessary trigger churn.

Provide explicit maintenance commands:

```text
metricule triggers plan
metricule triggers apply
metricule triggers status
metricule triggers remove
```

`remove` MUST only target identified Metricule-owned objects.

## 9.4 Performance

- Notifications should contain identifiers or empty payloads, not entire modified rows.
- Do not execute aggregate queries inside triggers.
- Use simple `pg_notify` calls.
- Avoid row-level triggers when statement-level triggers suffice.
- Provide documented trigger overhead expectations.
- Measure write overhead in integration benchmarks.

## 9.5 Failure behavior

Trigger creation failure MUST NOT corrupt normal collection.

If configured to require notifications, startup MUST report a clear error when notification setup fails.

Otherwise, explicitly permitted fallback polling MAY continue in degraded mode.

---

# 10. SQLite Adapter

## 10.1 Requirements

Support:

- Database file path.
- Read-only connection.
- Query timeout.
- Busy timeout.
- Existing WAL-mode databases.
- Concurrent application writers.
- Persistent connection for change-token observation.
- File replacement detection.
- Reconnect and recovery.

## 10.2 Change detection

Use `PRAGMA data_version` on one persistent observer connection.

Important:

The value is meaningful only when comparing successive readings from the same connection.

Do not compare data-version values from different connections.

## 10.3 Collection strategy

```text
Open SQLite database
       |
       v
Record initial data_version
       |
       v
Poll observer connection
       |
       v
data_version changed?
       |
   +---+---+
   |       |
  YES      NO
   |       |
   v       v
Invalidate Wait
dependent
collections
```

The change token MUST be treated as a hint that the database changed, not a log of individual transactions.

If multiple commits occur between checks, only the fact that a change occurred is observable.

## 10.4 Reconciliation

- Reconcile periodically even when the change token does not change.
- Detect file replacement and reopen when necessary.
- Handle database disappearance and restoration.
- Handle stale connection state.
- Avoid prolonged read transactions.
- Do not alter journal mode.
- Do not install SQLite triggers in v0.1.
- Do not write to the database.

SQLite paths MUST be configurable and compatible with shared Docker volumes.

---

# 11. HTTP/JSON Adapter

## 11.1 Required features

- HTTP and HTTPS.
- Configurable base URL.
- GET requests in v0.1.
- Static headers.
- Bearer token authentication.
- Basic authentication.
- Environment/file-based credentials.
- Request timeout.
- Response size limit.
- JSON parsing.
- JSON selectors.
- Optional query parameters.
- Status code validation.
- Bounded retries.
- Connection reuse.

## 11.2 Mapping examples

Scalar:

```yaml
mapping:
  value: "$.sessions.active"
```

Array:

```yaml
mapping:
  rows: "$.workers[*]"
  value: "$.active_tasks"
  labels:
    worker: "$.name"
```

JSON selector semantics MUST be precisely documented and backed by tests.

Selectors MUST behave consistently across nested arrays, missing fields, null values, and numeric conversions.

## 11.3 Safety requirements

- Do not follow redirects to unexpected origins by default.
- Bound response bytes.
- Bound redirect count.
- Avoid logging authorization headers.
- Reject non-finite numeric values.
- Enforce request timeouts.
- Do not expose generic outbound URL fetching through public HTTP endpoints.
- Maintain an explicit list of allowed paths or origins when configuration requires it.

## 11.4 Future enhancements

Not included in v0.1:

- SSE subscriptions.
- WebSocket subscriptions.
- Webhook ingestion.
- GraphQL request definitions.
- OAuth2 token refresh.
- XML parsing.
- gRPC reflection-based metrics extraction.

The architecture should allow these later without redesigning the scheduler.

---

# 12. Mapping Engine

The mapping engine MUST operate on normalized result sets.

## 12.1 Basic mappings

SQL row:

```text
status      value
pending     12
running      4
failed       2
```

Configuration:

```yaml
mapping:
  value: value
  labels:
    status: status
```

Exposition:

```text
app_jobs_by_status{status="pending"} 12
app_jobs_by_status{status="running"} 4
app_jobs_by_status{status="failed"} 2
```

## 12.2 Supported conversions

v0.1:

- Integer to numeric sample.
- Floating-point to numeric sample.
- Numeric strings with explicit coercion.
- Boolean to 0/1.
- Static labels.
- Labels extracted from result columns.
- JSON selector extraction.
- Configured unit scaling.
- Bounded state-set conversion.

## 12.3 Conversion errors

Invalid conversions MUST report:

- Metric name.
- Source ID.
- Mapping field.
- Error category.
- Non-sensitive diagnostic context.

Do not expose secret values or raw sensitive records.

## 12.4 Null handling

Default behavior: null numeric values fail the collection.

Allow an explicit alternative policy:

```yaml
mapping:
  value: value
  null: omit
```

Do not silently convert null to zero.

## 12.5 Expression engine

Not required for v0.1.

If implemented later, use a restricted, deterministic expression language without filesystem, network, process execution, or arbitrary host access.

Historical calculations and time-series aggregation should normally remain in PromQL.

---

# 13. Runtime Telemetry

Metricule MUST instrument itself.

Suggested metric families:

```text
metricule_build_info
metricule_source_up
metricule_collection_success
metricule_collection_duration_seconds
metricule_collection_errors_total
metricule_collection_refresh_total
metricule_collection_last_success_timestamp_seconds
metricule_collection_snapshot_age_seconds
metricule_collection_samples
metricule_collection_trigger_events_total
metricule_collection_coalesced_events_total
metricule_source_reconnects_total
metricule_mapping_errors_total
metricule_cardinality_rejections_total
metricule_http_requests_total
metricule_http_request_duration_seconds
```

Labels SHOULD include bounded identifiers such as:

- `source`
- `collection`
- `reason`
- `result`

Do not include unbounded error messages as labels.

All telemetry must have stable family definitions.

## 13.1 Health endpoints

`/healthz`:
- Process alive.
- HTTP server responsive.
- Does not require every upstream source to be healthy.

`/readyz`:
- Configuration compiled.
- Required subsystems initialized.
- Readiness determined by explicitly documented initial collection and failure policies.

`/metrics`:
- Always serves a valid exposition response if the exporter process itself is operational.
- Collection failures reflected through metricule telemetry rather than necessarily failing HTTP scrapes.

---

# 14. Security Model

## 14.1 Process privileges

Default container MUST:
- Run as non-root.
- Support a read-only root filesystem.
- Require no privileged mode.
- Require no Docker socket.
- Require no host PID namespace.
- Drop Linux capabilities where practical.
- Permit configurable writable temp directories only when necessary.

## 14.2 Datastore privileges

Query connections SHOULD use read-only accounts.

PostgreSQL trigger management SHOULD use separate explicitly authorized credentials or a separate provisioning invocation.

The collection runtime must not require schema-owner privileges.

## 14.3 Secret handling

- Support Docker secrets.
- Support environment references.
- Never print credentials.
- Never include tokens in metrics labels.
- Redact sensitive DSN components.
- Avoid dumping configuration with secrets expanded.

## 14.4 Network security

The metrics endpoint MUST be intended for trusted networks by default.

Support TLS and optional authentication through an established exporter web toolkit or documented reverse-proxy deployment.

No public administrative mutation endpoint in v0.1.

## 14.5 Query safety

Metricule cannot securely prove that arbitrary SQL is read-only by checking whether it starts with `SELECT`.

Require read-only database privileges and, where supported, read-only transaction enforcement.

No public API for submitting arbitrary SQL.

Bound query duration, result volume, and connection counts.

---

# 15. Configuration CLI

Required commands:

```text
metricule serve --config /etc/metricule/config.yaml

metricule validate \
  --config /etc/metricule/config.yaml

metricule validate \
  --config /etc/metricule/config.yaml \
  --online

metricule test \
  --config /etc/metricule/config.yaml \
  --metric app_jobs_pending

metricule config schema

metricule version
```

PostgreSQL trigger management commands are a later phase unless explicitly scheduled sooner.

## 15.1 Validate

Offline validation MUST NOT connect to upstream systems.

Online validation MAY connect to sources and check queries, selectors, and returned data types.

## 15.2 Test

The test command MUST:
- Execute the selected collection once.
- Print diagnostic timing.
- Show sanitized source information.
- Show resulting metric exposition.
- Report mapping errors.
- Exit nonzero on failure.

This is an essential usability feature, not an optional developer convenience.

---

# 16. Docker Deployment

Example Compose deployment:

```yaml
services:
  application:
    image: example/application:latest
    volumes:
      - application-data:/data
    networks:
      - monitoring

  metricule:
    image: ghcr.io/OWNER/metricule:latest
    user: "65532:65532"

    command:
      - serve
      - --config
      - /etc/metricule/config.yaml

    volumes:
      - ./metricule.yaml:/etc/metricule/config.yaml:ro
      - application-data:/data:ro

    environment:
      APP_DATABASE_URL: ${APP_DATABASE_URL}

    ports:
      - "127.0.0.1:9108:9108"

    read_only: true

    security_opt:
      - no-new-privileges:true

    cap_drop:
      - ALL

    restart: unless-stopped

    networks:
      - monitoring

volumes:
  application-data:

networks:
  monitoring:
```

The example assumes the image uses a compatible entrypoint and that the SQLite file is readable by UID 65532. Production examples MUST document volume permissions and SQLite read-only/WAL access requirements.

## 16.1 Image requirements

- Multi-stage builds.
- linux/amd64 and linux/arm64.
- Non-root execution.
- OCI metadata labels.
- Version and commit metadata.
- Tagged and immutable digest references.
- Release binaries.
- SBOM generation.
- Automated vulnerability scanning.

## 16.2 GitHub Actions

Required workflows:

1. PR validation and unit tests.
2. Integration tests.
3. Static analysis and linting.
4. Race detector.
5. Security and dependency scanning.
6. Multi-architecture image publishing.
7. Tagged release creation.

Publish release images to GHCR.

---

# 17. Repository Layout

```text
metricule/
├── .github/
│   └── workflows/
│       ├── ci.yaml
│       ├── integration.yaml
│       └── release.yaml
├── cmd/
│   └── metricule/
│       └── main.go
├── internal/
│   ├── config/
│   ├── runtime/
│   ├── source/
│   │   ├── postgres/
│   │   ├── sqlite/
│   │   └── httpjson/
│   ├── collect/
│   ├── mapping/
│   ├── snapshot/
│   ├── exporter/
│   ├── triggers/
│   ├── telemetry/
│   └── secrets/
├── pkg/
│   └── adapter/
├── schemas/
│   └── v1alpha1.schema.json
├── examples/
│   ├── postgres/
│   ├── sqlite/
│   ├── http/
│   └── compose/
├── docs/
│   ├── architecture.md
│   ├── configuration.md
│   ├── adapter-development.md
│   ├── security.md
│   ├── troubleshooting.md
│   └── adr/
├── tests/
│   ├── integration/
│   └── fixtures/
├── Dockerfile
├── Makefile
├── go.mod
├── README.md
├── LICENSE
└── AGENTS.md
```

Public adapter interfaces belong in `pkg/adapter` only if external adapter implementations are a deliberate supported goal. Otherwise, keep interfaces internal until their stability is established.

---

# 18. Multi-Agent Implementation Plan

Parallelize work, but honor architectural dependencies.

## Agent 0: Technical Lead / Integrator

**Owns:** Architecture, compatibility contracts, integration.

Responsibilities:
- Finalize the configuration contract.
- Define shared interfaces.
- Define source capabilities.
- Establish metric semantics.
- Establish repository conventions.
- Define dependency versions.
- Review interface-changing PRs.
- Coordinate phases.
- Verify end-to-end behavior.

Deliverables:
- Architecture document.
- ADRs.
- Interface packages.
- Initial repository scaffold.
- `AGENTS.md`.

**Acceptance:** Every implementation agent can work from stable, documented interfaces.

## Agent 1: Configuration & CLI

**Owns:** `internal/config`, CLI, schema.

Responsibilities:
- YAML parsing.
- Versioned schema.
- Strict validation.
- Secret references.
- `serve`, `validate`, `test`, `version`.
- Diagnostic errors.
- Configuration examples.

Dependencies: Agent 0.

Acceptance:
- Invalid files fail deterministically.
- Schema matches Go validation.
- Tests cover missing keys, invalid types, duplicate metrics, unknown keys, and invalid references.

## Agent 2: Scheduler & Snapshot Engine

**Owns:** `internal/collect`, `internal/snapshot`.

Responsibilities:
- Collection scheduling.
- Debouncing.
- Event coalescing.
- Worker limits.
- Retries.
- Atomic publication.
- Staleness.
- Lifecycle.
- Graceful shutdown.

Dependencies: Agent 0.

Acceptance:
- No overlapping collection for the same plan.
- Notifications during collection result in a subsequent refresh.
- Scrapes never wait for source collection.
- Failed updates do not partially overwrite snapshots.
- Race detector passes.

## Agent 3: Mapping & Prometheus Exporter

**Owns:** `internal/mapping`, `internal/exporter`.

Responsibilities:
- Typed mapping.
- Gauge/counter support.
- Label extraction.
- State-set mapping.
- Cardinality enforcement.
- Exposition.
- Health endpoints.
- Mapping validation.

Dependencies: Agents 0 and 2.

Acceptance:
- Valid Prometheus exposition.
- No duplicate series.
- Deterministic conversions.
- Correct stale-series lifecycle.
- Cardinality overflow handled atomically.

## Agent 4: PostgreSQL Adapter

**Owns:** `internal/source/postgres`.

Responsibilities:
- Connection management.
- SQL querying.
- Dedicated LISTEN connection.
- Notification dispatch.
- Reconnect.
- Cancellation.
- Source health.

Dependencies: Agent 0.

Acceptance:
- PostgreSQL polling works.
- Notification-driven refresh works.
- Reconnection causes reconciliation.
- Query cancellation behaves correctly.
- Database restarts do not require Metricule restart.

## Agent 5: SQLite Adapter

**Owns:** `internal/source/sqlite`.

Responsibilities:
- Read-only connections.
- SQL querying.
- Persistent change observer.
- `PRAGMA data_version`.
- File replacement.
- Busy handling.
- Concurrency tests.

Dependencies: Agent 0.

Acceptance:
- Works against an application-owned database file.
- Detects external committed changes.
- Does not modify the database.
- Recovers from temporary unavailability.
- Supports WAL-mode databases where filesystem permissions permit.

## Agent 6: HTTP/JSON Adapter

**Owns:** `internal/source/httpjson`.

Responsibilities:
- HTTP connection behavior.
- Authentication.
- Request configuration.
- JSON parsing.
- Selector evaluation.
- Response limits.
- Retries.

Dependencies: Agent 0.

Acceptance:
- Scalar and array metrics work.
- Missing fields handled correctly.
- HTTP errors reflected in collector health.
- Secrets are never logged.
- Large responses are rejected safely.

## Agent 7: Trigger Management

**Owns:** `internal/triggers`.

Responsibilities:
- Trigger SQL generation.
- Provisioning.
- Inspection.
- Ownership tracking.
- Dry-run planning.
- Removal.
- Permissions validation.

Dependencies: Agents 0 and 4.

Acceptance:
- Trigger installation is opt-in.
- Apply is idempotent.
- Existing unrelated triggers are unaffected.
- Notification occurs after committed writes.
- Rollbacks do not publish committed-change notifications.
- Safe removal targets only Metricule-owned objects.

## Agent 8: CI, Packaging & Documentation

**Owns:** Docker, CI, examples, documentation.

Responsibilities:
- Multi-platform builds.
- Release pipeline.
- Container security.
- Integration test environment.
- README.
- Configuration reference.
- Operational troubleshooting.
- Examples.

Dependencies: Agent 0, later integration with all agents.

Acceptance:
- Clean clone builds.
- CI validates every PR.
- Images run as non-root.
- Examples work as documented.
- Release image exposes metrics in the reference deployment.

## Agent 9: QA / Adversarial Reviewer

**Owns:** Cross-cutting validation.

Responsibilities:
- Concurrency testing.
- Failure injection.
- Security review.
- Cardinality abuse cases.
- Database restart tests.
- Metric correctness.
- Integration acceptance.

Dependencies: Integrated milestones.

Acceptance:
- All test scenarios pass.
- Findings contain reproducible failures.
- No unsupported advertised configuration remains.
- Critical and high-severity findings are resolved before release.

---

# 19. Implementation Milestones

## M0: Repository & Contracts

Deliver:
- Working Go module.
- Initial architecture.
- Interface definitions.
- Configuration schema.
- CLI scaffold.
- Build/test CI.

Exit conditions:
- Interfaces reviewed.
- Configuration example parses.
- All agents can begin independent implementation.

## M1: Polling MVP

Deliver:
- PostgreSQL SQL queries.
- SQLite SQL queries.
- HTTP/JSON querying.
- Gauge mapping.
- Scheduled polling.
- Snapshot-backed `/metrics`.
- Health endpoints.

Exit conditions:
- Three source adapters work.
- Sample collection requires no application modification.
- Prometheus successfully scrapes results.
- Unit/integration tests pass.

**This is the minimum viable release.**

## M2: Change-Aware Refresh

Deliver:
- PostgreSQL LISTEN/NOTIFY.
- SQLite data_version.
- Dependency invalidation.
- Debouncing.
- Coalescing.
- Periodic reconciliation.
- Reconnect handling.

Exit conditions:
- Datastore changes trigger refreshes.
- Notification loss is repaired through reconciliation.
- Refreshes do not cause uncontrolled query storms.
- Collection remains decoupled from scraping.

## M3: Safe Trigger Management

Deliver:
- PostgreSQL generated notification triggers.
- Plan/apply/status/remove commands.
- Ownership protections.
- Idempotency.
- Trigger management documentation.

Exit conditions:
- Explicit opt-in works.
- Managed triggers are safe to provision repeatedly.
- Unrelated database objects remain unchanged.

## M4: Production Hardening

Deliver:
- Advanced failure injection tests.
- Security validation.
- Operational telemetry.
- Production Compose examples.
- Cross-platform release.
- SBOM.
- Documentation.
- Performance benchmarks.

Exit conditions:
- All release acceptance criteria satisfied.
- Public configuration behavior documented.
- Release binaries and images reproducible.
- No critical known correctness problems.

## M5: Future Event Semantics

Deferred.

Possible features:
- Durable CDC.
- Source checkpoint persistence.
- Replay-safe event counters.
- Histogram bucket construction from persisted aggregate data.
- HTTP subscriptions.
- Redis adapter.
- MySQL adapter.
- Hot configuration reload.
- Adapter plugin SDK.

Do not begin M5 before core correctness and operational maturity.

---

# 20. Testing Requirements

## 20.1 Unit testing

Cover:
- Parsing and validation.
- Selector conversion.
- Label creation.
- Numeric conversion.
- Cardinality limits.
- Scheduler behavior.
- Notification coalescing.
- Snapshot atomicity.
- Backoff.
- Staleness policies.
- State-set mapping.

## 20.2 PostgreSQL integration

Test:
1. Insert modifies aggregate result.
2. Update modifies aggregate result.
3. Delete modifies aggregate result.
4. Transaction rollback does not produce committed-change notification.
5. Multiple writes produce coalesced refresh.
6. Listener disconnect.
7. Database restart.
8. Trigger provisioning twice.
9. Trigger removal.
10. Read-only credential behavior.
11. Collection timeout.
12. Rapid writes during slow queries.

## 20.3 SQLite integration

Test:
1. External writer commits.
2. Observer detects change.
3. Same-connection data-version semantics.
4. WAL mode.
5. Concurrent writes.
6. Busy database.
7. File removal.
8. File replacement.
9. Permission denial.
10. Reconciliation after connection recovery.

## 20.4 HTTP integration

Test:
1. Scalar JSON value.
2. Array extraction.
3. Authentication.
4. Missing field.
5. Null field.
6. Invalid number.
7. HTTP 500.
8. Request timeout.
9. Oversized body.
10. Redirect to disallowed origin.

## 20.5 Concurrency

Run with Go race detection.

Test simultaneous:
- Scrapes.
- Refreshes.
- Notifications.
- Failed collections.
- Source reconnects.
- Snapshot replacements.
- Graceful shutdown.

## 20.6 Performance

Benchmarks MUST measure:
- Scrape latency with 1,000 / 10,000 / 100,000 published series.
- Memory usage per published series.
- Query refresh latency.
- Refresh coalescing ratio.
- Notification-to-publication delay.
- PostgreSQL trigger write overhead.
- Startup time.
- Idle resource consumption.

Targets must be measured and documented, not invented.

Initial engineering targets:
- Scrapes involve no upstream I/O.
- Idle CPU usage is minimal.
- Scrapes remain responsive during source outages.
- Memory grows in a bounded, predictable relationship to configured series limits.
- Notification bursts do not create unbounded work queues.

---

# 21. End-to-End Acceptance Scenarios

## Scenario A: PostgreSQL application

Given:
- A running application using PostgreSQL.
- Read-only query credentials.
- A Metricule configuration containing an aggregate SQL metric.

When:
- Metricule starts.
- Prometheus requests `/metrics`.

Then:
- The configured metric is emitted.
- The value reflects committed database state after successful refresh.
- Scraping does not initiate SQL execution.

## Scenario B: SQLite application

Given:
- An SQLite file shared read-only with Metricule.
- A configured user-count gauge.

When:
- Another process inserts a row and commits.

Then:
- Metricule eventually detects the change.
- It refreshes the affected metric.
- The gauge reflects the updated count.
- Metricule does not alter the application database.

## Scenario C: PostgreSQL notification

Given:
- A configured LISTEN/NOTIFY metric.
- A valid notification producer.

When:
- A monitored table changes.

Then:
- Metricule receives the notification.
- The appropriate collection is invalidated.
- The aggregate is refreshed.
- The snapshot is updated atomically.

## Scenario D: Source outage

Given:
- A previously successful collection.

When:
- The source becomes unavailable.

Then:
- Collection failures are visible.
- Exporter HTTP remains responsive.
- Previously collected values follow the configured staleness policy.
- Metrics are not silently zeroed.

## Scenario E: High-cardinality result

Given:
- A metric with a configured limit of 100 series.

When:
- Its query returns 1,000 distinct labelsets.

Then:
- The collection is rejected.
- A diagnostic metric increments.
- The snapshot follows configured failure behavior.
- Memory growth remains bounded.

## Scenario F: Notification burst

Given:
- A watched PostgreSQL table.

When:
- The application performs a burst of writes.

Then:
- Metricule coalesces redundant refresh requests.
- Eventually publishes current state.
- Does not start one expensive query per notification.
- Does not starve reconciliation.

## Scenario G: Deployment

Given:
- A working Compose stack.

When:
- The Metricule sidecar launches.

Then:
- No privileged access is required.
- No application source-code changes are required.
- Prometheus can scrape the exporter.
- Configuration and permissions are documented.

---

# 22. Work Coordination Rules

These rules are binding on all coding agents.

1. Read this specification and `AGENTS.md` before modifying code.
2. Do not change shared interfaces without coordination with Agent 0.
3. Do not silently narrow supported functionality.
4. Do not stub declared capabilities and report them implemented.
5. Do not introduce direct dependencies between unrelated adapters.
6. Do not put business logic inside the HTTP handler.
7. Do not create metric families dynamically from unvalidated arbitrary input.
8. Do not add application-specific behavior to shared components.
9. Do not replace real integration tests with mocks when actual datastore behavior is under test.
10. Do not add unbounded queues or goroutine-per-notification designs.
11. Do not provision database objects without explicit configuration.
12. Do not rely on webhook or database notifications as a durable event history.
13. Do not expose credentials or sensitive records in logs.
14. Do not merge features without tests and documentation.
15. Do not report a milestone complete unless its acceptance criteria have passed.

## Pull request requirements

Every PR MUST include:

- Problem addressed.
- Architectural impact.
- Implementation summary.
- Test coverage.
- Configuration changes.
- Compatibility considerations.
- Known limitations.

Agents MUST produce reproducible evidence for behavioral claims.

## Definition of Done

A task is complete only when:

- The implementation exists.
- Compilation passes.
- Relevant tests pass.
- Race tests pass where concurrency is involved.
- Config behavior is validated.
- Documentation is updated.
- Known limitations are stated.
- No debug artifacts or placeholder implementations remain.

---

# 23. Required Documentation

Before public release, produce:

- `README.md`: project overview and quick start.
- `docs/architecture.md`: runtime architecture.
- `docs/configuration.md`: full configuration reference.
- `docs/adapter-development.md`: source adapter interface and extension guide.
- `docs/security.md`: privileges, secrets, exposure.
- `docs/troubleshooting.md`: collection failures and source behavior.
- `docs/metrics.md`: Metricule's own telemetry.
- `docs/triggers.md`: PostgreSQL trigger management.
- `docs/deployment.md`: Docker/Compose instructions.
- `docs/limitations.md`: observability and consistency limitations.

Every documented example MUST be exercised by automated tests or CI-supported smoke tests wherever feasible.

---

# 24. Explicit Architectural Decisions

These decisions are considered settled unless a demonstrable correctness issue requires revision.

**AD-001:** Go is the implementation language.

**AD-002:** YAML is the user-facing configuration format.

**AD-003:** Prometheus scraping and source collection are independent.

**AD-004:** Published metric snapshots are immutable during exposition.

**AD-005:** Change notifications are invalidation signals, not durable events.

**AD-006:** PostgreSQL trigger management is opt-in.

**AD-007:** SQLite collection is read-only in v0.1.

**AD-008:** Initial support targets PostgreSQL, SQLite, and HTTP/JSON.

**AD-009:** PromQL remains responsible for historical time-series computations.

**AD-010:** Missing or invalid samples are not silently coerced to zero.

**AD-011:** Label cardinality and source resource consumption must be bounded.

**AD-012:** Event-derived counters require durable semantics before being supported.

**AD-013:** No dashboard or web-based configuration UI is required.

**AD-014:** OCI container distribution is a first-class deliverable.

**AD-015:** The binary must also work outside containers.

---

# 25. Initial Execution Instructions for the Orchestrating Agent

You are the technical lead responsible for delivering Metricule as a production-quality open-source project.

Treat this specification as the implementation contract.

Your first actions:

1. Initialize the repository and Go module.
2. Create `AGENTS.md` containing the non-negotiable coordination rules.
3. Establish the architecture and ADRs.
4. Finalize the versioned configuration schema and shared interfaces.
5. Identify independent workstreams.
6. Delegate workstreams to specialized implementation agents.
7. Ensure all agents share the same contracts.
8. Implement milestone M1 first.
9. Integrate and test before moving to M2.
10. Continue through M4, resolving defects before declaring completion.

Do not stop after producing scaffolding, interfaces, or documentation.

The end product must be a working, tested, documented, containerized Prometheus exporter with the supported source adapters and collection modes.

Do not mistake successful compilation for successful implementation.

**Priority order: correctness, safety, operability, simplicity, performance, extensibility.**

Deliver a working product, not a beautifully organized collection of TODO comments.

---

# 26. Final Acceptance Checklist

- [ ] Single static-friendly Go binary
- [ ] Versioned YAML configuration
- [ ] Strict configuration validation
- [ ] Configuration JSON Schema
- [ ] Environment/file secret references
- [ ] PostgreSQL adapter
- [ ] SQLite adapter
- [ ] HTTP/JSON adapter
- [ ] Gauge mapping
- [ ] Persisted cumulative counter mapping
- [ ] Label extraction
- [ ] Numeric conversion
- [ ] Bounded state-set mapping
- [ ] Cardinality enforcement
- [ ] Independent collection scheduler
- [ ] Cached Prometheus exposition
- [ ] Atomic snapshot publication
- [ ] Collection retry/backoff
- [ ] Timeouts and cancellation
- [ ] Source reconnection
- [ ] PostgreSQL LISTEN/NOTIFY
- [ ] SQLite data_version detection
- [ ] Notification debouncing
- [ ] Periodic reconciliation
- [ ] Opt-in PostgreSQL trigger management
- [ ] Trigger SQL plan/apply/status/remove
- [ ] `/metrics`
- [ ] `/healthz`
- [ ] `/readyz`
- [ ] Internal telemetry
- [ ] `validate` CLI
- [ ] `test` CLI
- [ ] Graceful shutdown
- [ ] Non-root container
- [ ] Docker Compose examples
- [ ] linux/amd64 images
- [ ] linux/arm64 images
- [ ] GitHub Actions CI
- [ ] GHCR publishing
- [ ] Unit tests
- [ ] Integration tests
- [ ] Race detector clean
- [ ] Failure injection tests
- [ ] Security scanning
- [ ] SBOM
- [ ] Performance benchmark results
- [ ] Full user documentation

**Release gate:** Metricule is not production-ready until the full M0–M4 acceptance criteria are satisfied.

# End of Implementation Handoff

