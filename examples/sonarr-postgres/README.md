# Sonarr on PostgreSQL

Reads Sonarr's library straight from its PostgreSQL database and refreshes each metric
when the underlying table changes, using Metricule-managed `LISTEN`/`NOTIFY` triggers.
No API polling, no API key: a write in Sonarr reaches `/metrics` within the debounce
window.

This complements [`examples/sonarr`](../sonarr/README.md) and does not replace it. The
download queue, health checks, and disk space live in Sonarr's memory, not its
database, so only the API can report them. Run both if you want both.

Requires Sonarr v4 configured for PostgreSQL (`PostgresHost` and friends in
`config.xml`). A SQLite-backed Sonarr has no notification mechanism; use the API
example.

## Metrics

| Metric | Labels | Refreshed by writes to |
|---|---|---|
| `sonarr_series` | `status`, `monitored` | `Series` |
| `sonarr_wanted_missing_episodes` | | `Episodes`, `Series` |
| `sonarr_episode_files` | `quality` | `EpisodeFiles` |
| `sonarr_episode_file_bytes` | `quality` | `EpisodeFiles` |
| `sonarr_history_events_24h` | `event` | `History` |
| `sonarr_blocklist_items` | | `Blocklist` |
| `sonarr_pending_releases` | | `PendingReleases` |

Every label comes from a fixed set — an enum mapped in SQL or a quality title — so
cardinality does not grow with the library.

## Database roles

Two connections, two roles:

- **`SONARR_DATABASE_URL`** — a role with `SELECT` only, used for every metric query.
  Metricule also runs each query in a read-only transaction. A successful query
  commits and only a failed one rolls back, so `pg_stat_database` rollback-ratio
  alerts track real failures.
- **`SONARR_ADMIN_DATABASE_URL`** — the role that owns Sonarr's tables, normally the
  user in Sonarr's `config.xml`. Only a table owner can create a trigger on it.
  Metricule uses this connection once at startup to create the `metricule` schema,
  its notify function, and one trigger per watched table, then never again.

Create the read-only role on `sonarr-main` as the Sonarr user:

```sql
CREATE ROLE metricule_reader LOGIN PASSWORD 'change-me';
GRANT CONNECT ON DATABASE "sonarr-main" TO metricule_reader;
GRANT USAGE ON SCHEMA public TO metricule_reader;
GRANT SELECT ON "Series", "Episodes", "EpisodeFiles", "QualityDefinitions",
                "History", "Blocklist", "PendingReleases" TO metricule_reader;
```

Grant only those tables. Sonarr's database also holds indexer and download-client
settings, which include credentials.

## Run

```sh
export SONARR_DATABASE_URL='postgres://metricule_reader:change-me@postgres:5432/sonarr-main?sslmode=require'
export SONARR_ADMIN_DATABASE_URL='postgres://sonarr:sonarr-password@postgres:5432/sonarr-main?sslmode=require'
metricule validate --config examples/sonarr-postgres/metricule.yaml
metricule serve --config examples/sonarr-postgres/metricule.yaml
```

Scrape `:9711/metrics`:

```yaml
scrape_configs:
  - job_name: sonarr-postgres
    static_configs:
      - targets: ["metricule-sonarr:9711"]
```

Check one metric without starting the server or touching triggers:

```sh
metricule test --config examples/sonarr-postgres/metricule.yaml --metric sonarr_wanted_missing_episodes
```

Inspect or remove the managed triggers:

```sh
metricule triggers status --config examples/sonarr-postgres/metricule.yaml
metricule triggers remove --config examples/sonarr-postgres/metricule.yaml
```

## How refresh works

All metrics share the channel `metricule_sonarr_changed`. Metricule creates one
statement-level trigger per watched table; each sends the table name, such as
`public.Episodes`, as the payload, so only metrics watching that table re-run.

A series refresh in Sonarr updates episodes in many separate statements.
`debounce: 2s` collapses that burst into one query.

Notifications are invalidation hints, not a durable log. A notification sent while
Metricule is disconnected is lost, so every metric also has a `fallback_interval`
that re-runs it regardless.

## Known limitations

**Time-driven values lag by up to `fallback_interval`.** An episode becomes missing
when its air date passes, and a history event leaves the 24-hour window as time
passes. Neither is a write, so no trigger fires. Those two metrics use a 5-minute
fallback; lower it if you need faster updates.

**`sonarr_wanted_missing_episodes` approximates Sonarr's Wanted view.** It counts
monitored episodes of monitored series with no file whose `AirDateUtc` has passed,
excluding specials (season 0). Sonarr's own query can differ slightly between
versions. Use the API example's metric of the same name if the numbers must match
the UI exactly.

**No queue, health, or disk metrics.** These are not in the database. See
[`examples/sonarr`](../sonarr/README.md).

**Schema coupling.** The queries depend on Sonarr's internal schema, which is not a
public API. A Sonarr migration that renames a column fails the affected metric
visibly (`metricule_collection_errors_total`); it does not produce zeros. If a
watched table is renamed away, trigger provisioning fails as a whole and logs an
error at startup. Collection continues on `fallback_interval` alone.

**Sonarr migrations and triggers.** Triggers live on Sonarr's tables. If a migration
recreates a table, its trigger is dropped with it; restart Metricule to reapply it.
`cleanup_on_shutdown` is `false` so ordinary restarts do not churn triggers.

**`QualityDefinitions` changes do not trigger a refresh.** Renaming a quality is
rare; the change appears on the next fallback run.
