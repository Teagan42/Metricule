# Configuration reference

Metricule is configured by a single YAML document. The format is a versioned public
interface: **unknown keys are rejected**, not ignored, so a typo fails validation
instead of silently disabling a metric.

Validate a file before deploying it:

```sh
metricule validate --config /etc/metricule/config.yaml
```

`validate` is offline. It reads neither the environment nor the referenced secret
files, so it runs in CI without production credentials. Add `--online` to additionally
open each source. Add `--print-config` to print the effective configuration after
defaults are applied, with every secret reference redacted.

The machine-readable schema ships with the binary:

```sh
metricule config schema > v1alpha1.schema.json
```

Point your editor at it for completion and structural checking. The schema is
deliberately a **subset** of what `metricule validate` enforces: cross-field rules such
as "this metric's `source` must name a declared source" are beyond JSON Schema's reach.
See [ADR 0007](adr/0007-schema-validation-parity.md).

> **Status.** `metricule validate`, `metricule serve`, `metricule test`,
> `metricule config schema`, and `metricule version` work today. Polling, mapping,
> snapshot publication, and exposition are implemented. Change-driven refresh
> (`notify` and `auto`) validates but reconciles on `fallback_interval` until M2;
> managed triggers validate but provision nothing until M3.

## Document structure

```yaml
apiVersion: metricule.io/v1alpha1   # required
kind: Exporter                      # required
metadata: {}                        # optional, informational
server: {}                          # optional, defaulted
defaults: {}                        # optional, defaulted
sources: {}                         # required, at least one
metrics: []                         # required, at least one
```

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `apiVersion` | string | yes | — | Must be `metricule.io/v1alpha1`. Any other value is refused. |
| `kind` | string | yes | — | Must be `Exporter`. |
| `metadata` | object | no | `{}` | Informational only; never affects collection or exposition. |
| `server` | object | no | see below | Exposition endpoints. |
| `defaults` | object | no | see below | Inherited collection settings and resource bounds. |
| `sources` | map | yes | — | Named data sources. At least one. |
| `metrics` | list | yes | — | Output metric families. At least one. |

A file may contain exactly one YAML document. A second document would be silently
ignored, hiding half the configuration, so it is refused.

### `metadata`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `name` | string | no | `""` | Deployment name. |
| `description` | string | no | `""` | Free text. |

### `server`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `listen` | string | no | `0.0.0.0:9108` | Listen address for the HTTP server. |
| `metrics_path` | string | no | `/metrics` | Prometheus exposition path. |
| `health_path` | string | no | `/healthz` | Liveness path. |
| `readiness_path` | string | no | `/readyz` | Readiness path. |

Every path must begin with `/`, and no two may be equal.

## Defaults

`defaults` supplies values that metrics inherit when they omit the equivalent field.
Inheritance is per field, not per block: a metric that sets only `collect.interval`
still inherits `collect.timeout`.

### `defaults.collection`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `interval` | duration | no | `30s` | Poll period inherited by `collect.interval`. |
| `timeout` | duration | no | `10s` | Per-collection timeout inherited by `collect.timeout`. Also the default PostgreSQL `query_timeout`. |
| `reconcile_interval` | duration | no | `5m` | Reconciliation period inherited by `collect.fallback_interval` in `notify` and `auto` modes. |
| `debounce` | duration | no | `250ms` | Change-notification debounce inherited by `collect.debounce`. |
| `jitter` | duration | no | `0s` | Random schedule offset inherited by `collect.jitter`. Zero means every schedule fires on its exact tick. |

Durations are Go duration strings: `250ms`, `30s`, `5m`, `1h30m`. They must be quoted
strings, not bare numbers — `interval: 30` is refused, because a bare integer would
silently mean nanoseconds.

### `defaults.limits`

Bounds on resource consumption (P5, §8.7). Each must be greater than zero, and
`max_series_per_metric` must not exceed `max_total_series` — otherwise one metric could
consume the entire global budget.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `max_series_per_metric` | integer | no | `1000` | Maximum distinct labelsets one metric family may publish. |
| `max_rows_per_query` | integer | no | `10000` | Maximum rows read from one query or JSON array. |
| `max_concurrent_collections` | integer | no | `4` | Maximum collections running at once. |
| `max_total_series` | integer | no | `100000` | Global cap on published series. |
| `max_metrics` | integer | no | `1000` | Maximum configured metric families. Checked against the length of `metrics` at validation time. |
| `max_label_value_bytes` | integer | no | `1024` | Maximum length of a single label value. |

On overflow the affected collection fails atomically and its previous snapshot is kept
or dropped per the failure policy. Results are never silently truncated.

### `defaults.failure`

Staleness policy for a collection that fails (§8.4). Neither mode ever substitutes zero
for an unavailable value (P3, AD-010).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `mode` | enum | no | `retain` | `retain` keeps the last successful samples until `max_stale_age` elapses, then omits them. `drop` omits the affected samples immediately. |
| `max_stale_age` | duration | no | `5m` | Retention bound for `retain`. Ignored by `drop`. |

Choose `drop` when a stale reading is more misleading than an absent one — a queue depth
or a file size, for example. Choose `retain` when brief source unavailability should not
create gaps in a slow-moving series.

## Secret references

Credential-bearing fields are never plain strings. They are reference objects with
**exactly one** of three keys (§5.3):

```yaml
dsn:
  env: APP_DATABASE_URL          # read from the environment
dsn:
  file: /run/secrets/app_db_url  # read from a file; a trailing newline is stripped
dsn:
  value: postgres://localhost/x  # literal, development only
```

Setting none is an error ("secret reference is empty"); setting more than one is an
error ("secret reference is ambiguous").

Resolution happens at startup, not during validation. `metricule validate` checks the
*shape* of a reference, so a missing environment variable or an unreadable file surfaces
when `metricule serve` starts, not in CI.

Resolved values never appear in logs, validation diagnostics, HTTP responses, error
messages, or `--print-config` output. `--print-config` renders a reference as
`env:APP_DATABASE_URL` or `file:/run/secrets/app_db_url`; a literal becomes
`literal:[REDACTED]`. That output is for reading, not for reloading: the reference form
is a scalar where the loader expects a mapping, so a dump is deliberately not a valid
input document.

There is no general `${VARIABLE}` expansion. Reference objects are the only indirection,
which keeps the set of values that can come from outside the file explicit and
auditable.

## Sources

`sources` is a map from name to source definition. The name identifies the source in
metric definitions and appears as a label value in Metricule's own telemetry, so it must
match `^[a-zA-Z_][a-zA-Z0-9_]*$` — `app_db` is valid, `app-db` is not.

`type` is the discriminator and is required. The remaining keys are validated against
the declared type, so a `sqlite` source rejects a `pool` block rather than ignoring it.

| `type` | Adapter | `poll` | `notify` | `auto` | Trigger management |
|---|---|---|---|---|---|
| `postgres` | PostgreSQL | yes | yes (`LISTEN`/`NOTIFY`) | yes | yes, opt-in |
| `sqlite` | SQLite | yes | no | yes (`PRAGMA data_version`) | no |
| `http` | HTTP/JSON | yes | no | yes (falls back to polling) | no |

`notify` on a source type that does not advertise notifications is refused at
validation time, before any connection is opened.

### `type: postgres`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `dsn` | secret ref | yes | — | Connection string. Use a role with `SELECT` only (§14.2). |
| `pool.max_open` | integer | no | `4` | Maximum open connections. Must be positive. |
| `pool.max_idle` | integer | no | `2` | Maximum idle connections. Must not be negative, and must not exceed `max_open`. |
| `query_timeout` | duration | no | `defaults.collection.timeout` | Statement timeout for collection queries. |
| `tls.mode` | enum | no | libpq default | One of `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`. Maps to libpq `sslmode`. |
| `tls.root_cert` | string | no | `""` | CA certificate path. |
| `tls.cert` | string | no | `""` | Client certificate path. |
| `tls.key` | string | no | `""` | Client key path. |
| `triggers` | object | no | absent — disabled | Opt-in managed notification triggers (AD-006). |
| `triggers.manage` | boolean | yes within `triggers` | — | `true` provisions triggers. Nothing is created unless this is set. |
| `triggers.schema` | string | no | `metricule` | Schema that holds the generated objects. Must be a plain identifier. |
| `triggers.cleanup_on_shutdown` | boolean | no | `false` | Removes the managed objects on exit. Off by default to avoid trigger churn across restarts. |
| `triggers.dsn` | secret ref | no | falls back to `dsn` | Separate provisioning credentials. Omitting it warns, because the collection role should not hold schema-owner privileges. |

Trigger management is scheduled for M3. Setting `manage: true` validates today but
provisions nothing yet.

### `type: sqlite`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `path` | string | yes | — | Path to the database file. |
| `readonly` | boolean | no | `true`, always | SQLite collection is read-only in v0.1 (AD-007). The value is forced to `true`. |
| `busy_timeout` | duration | no | `3s` | How long to wait on a locked database. Must be positive. |
| `change_check_interval` | duration | no | `2s` | How often the observer connection reads `PRAGMA data_version`. Must be positive. |

Mount the file read-only. A WAL database also needs its `-wal` and `-shm` siblings
readable, and the containing directory must be readable by the exporter's UID.

`data_version` only reports database-wide change, so any commit invalidates every plan
that depends on the source (§7.4).

### `type: http`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `base_url` | string | yes | — | Scheme must be `http` or `https`, and a host is required. |
| `timeout` | duration | no | `5s` | Request timeout. Must be positive. |
| `headers` | map | no | `{}` | Static headers sent on every request. |
| `authentication` | object | no | none | See below. |
| `allowed_paths` | list | no | `[]` — any path | Request allow-list. When non-empty, a metric may only request a listed path, exactly. Each entry must begin with `/`. |
| `max_response_bytes` | integer | no | `4194304` (4 MiB) | Response body read limit (§11.3). Must be positive. |
| `max_redirects` | integer | no | `0` | Redirect budget. Zero follows none. Cross-origin redirects are refused regardless of this value, because a redirect can carry a credentialed request to another origin. |
| `retries` | integer | no | `2` | Retry attempts per collection. Must not be negative. |

Set `allowed_paths` whenever the configuration is not fully trusted. Without it, any
path under `base_url` is reachable, which turns the exporter into a general outbound
fetcher driven by its own config file.

#### `authentication`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `type` | enum | yes | — | `bearer` or `basic`. |
| `token` | secret ref | for `bearer` | — | Sent as `Authorization: Bearer`. Rejected for `basic`. |
| `username` | string | for `basic` | — | Rejected for `bearer`. |
| `password` | secret ref | for `basic` | — | Rejected for `bearer`. |

Only GET is supported in v0.1, so there is no request body to sign and no OAuth2 refresh
flow.

## Metrics

`metrics` is a list. Each entry declares one output family and how to collect it.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `name` | string | yes | — | Prometheus metric name. See naming rules below. |
| `help` | string | yes | — | Family help text. Must not be empty. |
| `type` | enum | yes | — | `gauge`, `counter`, or `state_set`. |
| `source` | string | yes | — | Name of a declared source. |
| `query` | object | for SQL sources | — | SQL definition. Rejected for `http` sources. |
| `request` | object | for `http` sources | — | HTTP request definition. Rejected for SQL sources. |
| `mapping` | object | yes | — | How a result becomes samples. |
| `collect` | object | no | `mode: poll` with inherited timings | Scheduling. |
| `limits` | object | no | inherits `defaults.limits` | Per-metric bounds, inherited field by field. |
| `failure` | object | no | inherits `defaults.failure` | Per-metric staleness policy. |

### Metric types

- **`gauge`** — a point-in-time value that may rise and fall.
- **`counter`** — requires a *persisted monotonic cumulative* source value (§8.5). A
  `SELECT COUNT(*)` of current rows is a gauge even when its name ends in `_total`,
  because deleting rows would make it fall. Reading a ledger's running sum is a counter.
  Counter names that do not end in `_total` produce a warning, not an error.
- **`state_set`** — emits one gauge series per configured state, carrying a generated
  `state` label: `1` for the matching state and `0` for the rest. A Metricule mapping
  abstraction, not a separate Prometheus wire type. `mapping.states` is required, and
  bounds state cardinality by configuration so source data cannot grow it.

Histograms and summaries are not supported in v0.1.

### Naming rules

- `name` must match `^[a-zA-Z_:][a-zA-Z0-9_:]*$`.
- The `metricule_` prefix is reserved for Metricule's own telemetry.
- The `_bucket` and `_sum` suffixes are reserved for histogram and summary families.
- Label names must match `^[a-zA-Z_][a-zA-Z0-9_]*$`, and the `__` prefix is reserved by
  Prometheus.
- Two metrics may not share a name.
- Names that are not lowercase produce a warning. Prefer lowercase `snake_case`, a
  namespace prefix, base units (`_seconds`, `_bytes`), and variable dimensions in labels
  rather than in names (§8.6).

### `query` (SQL sources)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `sql` | string | yes | — | The statement to execute. Must not be blank. |

Write one aggregate query per metric and let the database do the aggregation. SQL syntax
and result shape are not checked offline — that needs a live connection, which is what
`metricule validate --online` is for.

### `request` (HTTP sources)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `method` | string | no | `GET` | Only `GET` is supported in v0.1. |
| `path` | string | yes | — | Must begin with `/`, and must appear in the source's `allowed_paths` when that list is non-empty. |
| `params` | map | no | `{}` | Query-string parameters. |
| `headers` | map | no | `{}` | Per-request headers, in addition to the source's. |

### `mapping`

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `value` | string | yes | — | Column name for SQL sources; RFC 9535 JSONPath for HTTP sources. |
| `rows` | string | no | `""` | JSONPath selecting repeating elements for multi-series results. **HTTP sources only** — SQL results are already row-oriented. Empty means the result is one row. |
| `labels` | map | no | `{}` | Label name to column name (SQL) or selector (JSON). |
| `static_labels` | map | no | `{}` | Labels attached verbatim to every series. A name may not appear in both `labels` and `static_labels`. |
| `states` | list | for `state_set` | — | Allowed state values. Must be non-empty and free of duplicates. Rejected for `gauge` and `counter`. |
| `scale` | number | no | `1` | Multiplier for unit conversion, applied to the extracted value. |
| `null` | enum | no | `fail` | Null handling: `fail` fails the collection, `omit` drops the affected sample and keeps the rest. |

Null is never coerced to zero (§12.4, AD-010). `fail` is the default precisely because
a fabricated zero is indistinguishable from a real one on a dashboard.

Selectors are RFC 9535 JSONPath, evaluated by `internal/selector`. Inside a `rows`
iteration, `value` and `labels` selectors are evaluated **relative to each selected
row**:

```yaml
mapping:
  rows: "$.workers[*]"
  value: "$.active_tasks"
  labels:
    worker: "$.name"
```

A selector used for a scalar must match exactly one value. Matching several is an error
rather than a silent "first match", because that would make sample values depend on JSON
ordering. Matching nothing is distinct from matching a present `null`: the null policy
applies only to the latter.

#### The `null:` key

YAML resolves a bare `null` mapping key to the null scalar rather than to the string
`"null"`. Metricule accepts the spelling the specification uses:

```yaml
mapping:
  value: value
  null: omit
```

Quoting it as `"null": omit` also works. This is the only key in the format with that
quirk; it is recorded in [ADR 0007](adr/0007-schema-validation-parity.md) because it
surprises people.

### `collect`

Omit the whole block and the metric polls on the inherited interval.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `mode` | enum | no | `poll` | `poll`, `notify`, or `auto`. |
| `interval` | duration | no | `defaults.collection.interval` | Poll period. `poll` mode only. |
| `timeout` | duration | no | `defaults.collection.timeout` | Bounds one collection. Must be positive. A timeout that is not shorter than `interval` produces a warning, because collections can then overlap continuously. |
| `channel` | string | for `notify` | — | PostgreSQL `NOTIFY` channel. Rejected in `poll` mode. |
| `fallback_interval` | duration | no | `defaults.collection.reconcile_interval` | Reconciliation period. Required and always set for `notify` and `auto`. |
| `debounce` | duration | no | `defaults.collection.debounce` | Collapses a burst of change signals into one refresh. Must not be negative. |
| `jitter` | duration | no | `defaults.collection.jitter` | Random offset added to scheduled collections, so unrelated metrics do not all query on the same tick. Must not be negative. |
| `watch.tables` | list | no | `[]` | Narrows change observation to these tables. `notify` and `auto` only. |
| `watch.events` | list | no | all | DML kinds to react to: `insert`, `update`, `delete`. `notify` and `auto` only. |

#### Modes

- **`poll`** — refresh every `interval`. Works with every source type. The only mode
  that needs no source support.
- **`notify`** — refresh when the source signals a change, *plus* reconcile every
  `fallback_interval`. Requires a source type that advertises notifications, which today
  means `postgres`. Reconciliation is mandatory and not optional tuning: notifications
  are invalidation signals, not a durable event log (AD-005), so a missed signal must be
  repaired by a periodic sweep.
- **`auto`** — use the best mechanism the adapter advertises, falling back through
  notifications, change tokens, and polling. On `sqlite` this selects `data_version`
  observation. `fallback_interval` applies as for `notify`.

`manual` and `cdc` appear in the specification as reserved names and are **rejected**
in v0.1 rather than accepted and ignored.

## Validation

Validation runs in two stages. Decoding comes first and reports syntax, type and
unknown-key problems; a file that fails to decode never reaches the semantic stage.
Semantic validation then reports *every* problem it finds in one pass, sorted by
configuration path, so the same invalid file always fails identically. Errors block
loading; warnings do not.

```text
error: metrics[1].name: duplicate metric name "app_users", already declared by metrics[0]
error: metrics[2].collect.mode: source type "sqlite" does not support notifications; use mode "auto" to use its change-token detection
warning: metrics[3].name: counters conventionally end with _total
```

Detected classes (§5.5):

| Class | Examples |
|---|---|
| Structure | unknown keys at any level, wrong YAML types, a second document, keys belonging to another source type |
| Required fields | `apiVersion`, `kind`, at least one source, at least one metric, `name`, `help`, `type`, `source`, `mapping.value` |
| Duplicates and conflicts | duplicate metric names, duplicate states, a label in both `labels` and `static_labels`, colliding server paths |
| References | a metric naming an undeclared source, a request path outside `allowed_paths`, a source type with no adapter in this build |
| Naming | invalid metric and label names, the `metricule_` prefix, `_bucket` and `_sum` suffixes, the `__` label prefix, the generated `state` label |
| Collection | unsupported or reserved modes, `notify` without a channel, `channel` or `watch` in the wrong mode, unsupported watch events, negative durations |
| Adapter capability | `notify` on a source type that does not support it, a `query` block on an HTTP source, a `request` block on a SQL source, a non-GET method |
| Credentials | empty or ambiguous secret references, `bearer` without a token, `basic` without a username or password, mismatched authentication fields |
| Selectors | malformed JSONPath in `value`, `rows`, or `labels` |
| Limits | non-positive limits, `max_series_per_metric` above `max_total_series`, more metrics than `max_metrics`, an idle pool above the open pool |

Not checked offline, by design: SQL syntax, result column names and types, JSON response
shape, and source reachability. Those need a live source — use
`metricule validate --online`.

## Known limitations

- **Change-driven refresh is not implemented.** `mode: notify` and `mode: auto`
  validate, but until M2 they refresh only on `fallback_interval`. A SQLite `auto`
  plan therefore polls rather than observing `PRAGMA data_version`; a PostgreSQL
  `notify` plan reconciles rather than listening. See
  [`limitations.md`](limitations.md#mode-notify-currently-degrades-to-reconcile-only).
- **Trigger management validates but does nothing.** `triggers.manage: true` is
  accepted and ignored until M3.
- Histograms, summaries, event-derived counters, expression evaluation, non-GET HTTP
  methods, and the `manual` and `cdc` collection modes are out of scope for M1.

## Examples

Validated in CI by `make validate-examples`, which runs `metricule validate` over
`examples/*.yaml` and `examples/*/*.yaml`:

| File | Shows |
|---|---|
| [`examples/reference.yaml`](../examples/reference.yaml) | The specification's §5.1 contract example |
| [`examples/postgres/metricule.yaml`](../examples/postgres/metricule.yaml) | Polling, `LISTEN`/`NOTIFY` with watch scope, a ledger-backed counter, TLS |
| [`examples/sqlite/metricule.yaml`](../examples/sqlite/metricule.yaml) | Read-only access, `auto` mode change detection, per-metric limits and `drop` policy |
| [`examples/http/metricule.yaml`](../examples/http/metricule.yaml) | Bearer authentication, a path allow-list, array extraction, unit scaling, `state_set` |

`schemas/testdata/` holds the fixtures behind the schema parity tests, split into
`valid`, `invalid-structural`, and `invalid-semantic`.
