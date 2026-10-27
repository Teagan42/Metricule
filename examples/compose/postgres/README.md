# PostgreSQL + Metricule + Prometheus

A production-shaped stack exercising the full PostgreSQL path:

- a **read-only** collection role
- a **separate provisioning role** for managed triggers
- opt-in `LISTEN/NOTIFY` with a table-level managed trigger
- a polling aggregate metric alongside the change-driven one
- Prometheus scraping the exporter

## Files

| File | Purpose |
|---|---|
| `compose.yaml` | PostgreSQL, Metricule, Prometheus, healthchecks, limits |
| `init.sh` | Creates the app table, seeds rows, creates the `reader` role |
| `metricule.yaml` | Poll and notify metrics, managed trigger configuration |
| `prometheus.yml` | Scrapes `metricule:9108` |

## Run

```console
$ docker compose up -d --build
$ curl -s localhost:9108/metrics | grep app_jobs
app_jobs_pending 2
app_jobs_by_state{state="failed"} 1
app_jobs_by_state{state="queued"} 2
app_jobs_by_state{state="running"} 1
```

Prometheus is at <http://localhost:9090>. Try
`app_jobs_by_state{state="running"}`. The first value appears within one scrape
interval after startup.

Metricule applies the managed trigger at startup because `triggers.manage: true`.
Verify the database objects:

```console
$ docker compose exec postgres psql -U owner -d app -c '\d public.jobs'
```

The `metricule_notify_<hash>` trigger calls `metricule.metricule_notify_change()`.

## Verify change-driven refresh

```console
$ docker compose exec postgres psql -U owner -d app \
    -c "UPDATE public.jobs SET state = 'running' WHERE state = 'queued'"
```

`app_jobs_by_state` updates on the next collection. The managed trigger sends
`NOTIFY metricule_jobs_changed` with the qualified table name as the payload; the
`debounce: 250ms` window collapses a burst into one refresh. `fallback_interval: 60s`
repairs a notification lost while Metricule was disconnected.

## Why two DSNs

`APP_READ_DSN` uses the `reader` role: `SELECT` only. `APP_ADMIN_DSN` uses the database
owner, which is only used by `internal/triggers` on a separate connection for DDL.
Collection never runs on the provisioning connection, and the provisioning role never
runs metric queries.

If you omit `triggers.dsn`, validation warns: the read-only role would otherwise need
schema-owner privileges, defeating the point of the split.

## Production notes

- Replace the default passwords. Prefer `_FILE` secret mounts or an external secret
  store; this example keeps them literal so it works out of the box.
- Pin the Metricule image by digest, not by a moving tag.
- Do not publish PostgreSQL on a host interface. The compose file binds both published
  ports to `127.0.0.1` for local use.
- Add TLS to the DSNs (`sslmode=verify-full`) when the database is not on a private
  network.
- `cleanup_on_shutdown` is `false`: restarts should not drop and recreate triggers.
  Use `metricule triggers remove` for deliberate removal.
- The container runs read-only with all capabilities dropped and a 256 MB limit. Raise
  the limit if your series count needs it; see
  [`docs/performance.md`](../../../docs/performance.md).

## Teardown

```console
$ docker compose down -v
```

`-v` removes the PostgreSQL data volume and Prometheus storage.
