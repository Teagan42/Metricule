# MySQL + Metricule + Prometheus

A production-shaped stack exercising the MySQL adapter:

- a `SELECT`-only application user
- read-only transactions for every collection
- polling aggregate and grouped metrics
- Prometheus scraping the exporter

## Files

| File | Purpose |
|---|---|
| `compose.yaml` | MySQL, Metricule, Prometheus, healthcheck, limits |
| `init.sh` | Creates the app table, seeds rows, creates the `reader` user |
| `metricule.yaml` | MySQL source and two polling metrics |
| `prometheus.yml` | Scrapes `metricule:9108` |

## Run

```console
$ docker compose up -d --build
$ curl -s localhost:9108/metrics | grep app_jobs
app_jobs_by_state{state="failed"} 1
app_jobs_by_state{state="queued"} 2
app_jobs_by_state{state="running"} 1
app_jobs_pending 2
```

Prometheus is at <http://localhost:9090>.

Verify the read-only user cannot write:

```console
$ docker compose exec mysql mysql -ureader -preader-secret -e \
    "UPDATE app.jobs SET state='done' WHERE id=1"
ERROR 1142 (42000): UPDATE command denied to user 'reader'@'%' for table 'jobs'
```

Even with broader grants, every collection runs in
`START TRANSACTION READ ONLY`, so the server rejects writes from the adapter.

## Production notes

- Replace the default passwords and use Docker secrets.
- Pin the Metricule image by digest.
- MySQL has no adapter-level TLS block: add `tls=true` or `tls=verify-full` (with a
  registered CA) to `MYSQL_DSN`.
- Keep `multiStatements` disabled. The adapter refuses a DSN that enables it.
- Metricule uses polling because MySQL notifications and binlog CDC are not
  implemented. Preference for near-real-time refresh should move to PostgreSQL.
- The container runs read-only with all capabilities dropped and a 256 MB limit.
  Raise the limit for larger series counts; see
  [`docs/performance.md`](../../../docs/performance.md).

## Teardown

```console
$ docker compose down -v
```

`-v` removes the MySQL data volume and Prometheus storage.
