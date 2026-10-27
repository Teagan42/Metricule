# SQLite sidecar + Metricule + Prometheus

Metricule beside an application that owns a SQLite file. The stack demonstrates:

- read-only collection enforced by the connection (`mode=ro`)
- `mode: auto` change detection through `PRAGMA data_version`
- a read-write mount for the WAL `-shm` sidecar while the adapter stays read-only
- Prometheus scraping the exporter

## Files

| File | Purpose |
|---|---|
| `compose.yaml` | Metricule and Prometheus, read-only root filesystem, limits |
| `metricule.yaml` | SQLite source, `auto` collection, change observation |
| `seed.sql` | Creates `data/app.db` and seeds a few rows |
| `prometheus.yml` | Scrapes `metricule:9108` |

## Run

The database belongs to the application, so this stack does not create it:

```console
$ mkdir -p data
$ sqlite3 data/app.db < seed.sql
$ docker compose up -d --build
$ curl -s localhost:9108/metrics | grep app_users
app_users 3
```

Then write through the application's path (on the host, for this example):

```console
$ sqlite3 data/app.db "INSERT INTO users (id) VALUES (4)"
$ sleep 2
$ curl -s localhost:9108/metrics | grep app_users
app_users 4
```

`change_check_interval: 1s` sets how often the pinned observer reads
`PRAGMA data_version`. The value is connection-local, so the adapter keeps one
dedicated connection for comparison. Any commit invalidates all `auto` plans on the
source, because SQLite cannot name the changed table.

Prometheus is at <http://localhost:9090>.

## Why the data mount is read-write

The adapter opens every connection with `mode=ro` and never writes the database
(AD-007). But a live WAL database needs a `-shm` coordination file beside it, which
SQLite creates even for read-only readers. Mount the directory writable and let the
connection mode enforce the guarantee; a fully read-only mount only works for
rollback-journal databases or cleanly closed WAL files.

The container runs as uid 65532. The database and its parent directory must be
readable by that uid, and the directory must permit creating `-shm`.

## Production notes

- Run Metricule as a sidecar in the application's pod or compose stack, with the data
  directory mounted from the application.
- Keep `readonly: true`. The adapter refuses to start otherwise.
- Do not scrape more often than the metric needs; the exporter does no I/O, but a
  large series set still costs CPU.
- Use `fallback_interval` even with `auto`: a missed `data_version` observation is
  repaired by reconciliation.
- Pin the image by digest and keep the container read-only as shown.

## Teardown

```console
$ docker compose down -v
```

The `data/` directory is a bind mount and is not removed by `down -v`.
