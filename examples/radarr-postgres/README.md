# Radarr on PostgreSQL

Reads Radarr's library straight from its PostgreSQL database and refreshes each metric
when the underlying table changes, using Metricule-managed `LISTEN`/`NOTIFY` triggers.
No API polling, no API key: a write in Radarr reaches `/metrics` within the debounce
window.

This complements [`examples/radarr`](../radarr/README.md) and does not replace it. The
download queue, health checks, and disk space live in Radarr's memory, not its
database, so only the API can report them. Run both if you want both.

Requires Radarr v4 or later configured for PostgreSQL (`PostgresHost` and friends in
`config.xml`). A SQLite-backed Radarr has no notification mechanism; use the API
example.

## Metrics

| Metric | Labels | Refreshed by writes to |
|---|---|---|
| `radarr_movies` | `status`, `monitored` | `Movies`, `MovieMetadata` |
| `radarr_wanted_missing_movies` | | `Movies`, `MovieMetadata` |
| `radarr_movie_files` | `quality` | `MovieFiles` |
| `radarr_movie_file_bytes` | `quality` | `MovieFiles` |
| `radarr_history_events_24h` | `event` | `History` |
| `radarr_blocklist_items` | | `Blocklist` |
| `radarr_pending_releases` | | `PendingReleases` |

Every label comes from a fixed set — an enum mapped in SQL or a quality title — so
cardinality does not grow with the library.

## Database roles

Two connections, two roles:

- **`RADARR_DATABASE_URL`** — a role with `SELECT` only, used for every metric query.
  Metricule also runs each query in a read-only transaction.
- **`RADARR_ADMIN_DATABASE_URL`** — the role that owns Radarr's tables, normally the
  user in Radarr's `config.xml`. Only a table owner can create a trigger on it.
  Metricule uses this connection once at startup to create the `metricule` schema,
  its notify function, and one trigger per watched table, then never again.

Create the read-only role on `radarr-main` as the Radarr user:

```sql
CREATE ROLE metricule_reader LOGIN PASSWORD 'change-me';
GRANT CONNECT ON DATABASE "radarr-main" TO metricule_reader;
GRANT USAGE ON SCHEMA public TO metricule_reader;
GRANT SELECT ON "Movies", "MovieMetadata", "MovieFiles", "QualityDefinitions",
                "History", "Blocklist", "PendingReleases" TO metricule_reader;
```

Grant only those tables. Radarr's database also holds indexer and download-client
settings, which include credentials.

## Run

```sh
export RADARR_DATABASE_URL='postgres://metricule_reader:change-me@postgres:5432/radarr-main?sslmode=require'
export RADARR_ADMIN_DATABASE_URL='postgres://radarr:radarr-password@postgres:5432/radarr-main?sslmode=require'
metricule validate --config examples/radarr-postgres/metricule.yaml
metricule serve --config examples/radarr-postgres/metricule.yaml
```

Scrape `:9712/metrics`:

```yaml
scrape_configs:
  - job_name: radarr-postgres
    static_configs:
      - targets: ["metricule-radarr:9712"]
```

Check one metric without starting the server or touching triggers:

```sh
metricule test --config examples/radarr-postgres/metricule.yaml --metric radarr_wanted_missing_movies
```

Inspect or remove the managed triggers:

```sh
metricule triggers status --config examples/radarr-postgres/metricule.yaml
metricule triggers remove --config examples/radarr-postgres/metricule.yaml
```

## How refresh works

All metrics share the channel `metricule_radarr_changed`. Metricule creates one
statement-level trigger per watched table; each sends the table name, such as
`public.Movies`, as the payload, so only metrics watching that table re-run.

A library refresh in Radarr updates movies in many separate statements.
`debounce: 2s` collapses that burst into one query.

Notifications are invalidation hints, not a durable log. A notification sent while
Metricule is disconnected is lost, so every metric also has a `fallback_interval`
that re-runs it regardless.

## Known limitations

**`radarr_wanted_missing_movies` approximates Radarr's availability rule.** It counts
monitored movies with no file whose release status has reached their minimum
availability. Radarr's own check also weighs release dates and the availability
delay setting, so the two can differ by a few movies around a release. Use the API
example's metric of the same name if the numbers must match the UI exactly.

**History lags by up to `fallback_interval`.** An event leaves the 24-hour window as
time passes, which is not a write, so no trigger fires. That metric uses a 5-minute
fallback; lower it if you need faster updates.

**No queue, health, or disk metrics.** These are not in the database. See
[`examples/radarr`](../radarr/README.md).

**Schema coupling.** The queries depend on Radarr's internal schema, which is not a
public API. A Radarr migration that renames a column fails the affected metric
visibly (`metricule_collection_errors_total`); it does not produce zeros. If a
watched table is renamed away, trigger provisioning fails as a whole and logs an
error at startup. Collection continues on `fallback_interval` alone.

**Radarr migrations and triggers.** Triggers live on Radarr's tables. If a migration
recreates a table, its trigger is dropped with it; restart Metricule to reapply it.
`cleanup_on_shutdown` is `false` so ordinary restarts do not churn triggers.

**`QualityDefinitions` changes do not trigger a refresh.** Renaming a quality is
rare; the change appears on the next fallback run.
