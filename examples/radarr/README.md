# Radarr

Replaces [Varken](https://github.com/Boerderij/Varken)'s Radarr collector with a
declarative Metricule configuration. No InfluxDB, and no Python service: Prometheus
scrapes Metricule, Metricule polls the Radarr API.

## Run

```sh
export RADARR_API_KEY="$(grep -oP '(?<=<ApiKey>)[^<]+' /config/config.xml)"
metricule validate --config examples/radarr/metricule.yaml
metricule serve --config examples/radarr/metricule.yaml
```

The API key is in Radarr under *Settings → General → API Key*, or in `config.xml`.
Point `base_url` at your instance and scrape `:9710/metrics`.

```yaml
scrape_configs:
  - job_name: radarr
    static_configs:
      - targets: ["metricule-radarr:9710"]
```

Check one metric without starting the server:

```sh
metricule test --config examples/radarr/metricule.yaml --metric radarr_queue_items
```

## Authentication

Radarr accepts the API key only as an `X-Api-Key` header or an `apikey` query
parameter — it rejects `Authorization`. The config uses `type: api_key`, which keeps
the key in a secret reference:

```yaml
authentication:
  type: api_key
  header: X-Api-Key
  token:
    env: RADARR_API_KEY
```

Use `file:` instead of `env:` to read it from a mounted secret. Putting the key in the
source `headers` map would work but stores a credential in plain configuration; the
`apikey` query parameter would additionally put it in request URLs. Neither is
recommended.

`max_redirects` is 0 because a redirect would carry `X-Api-Key` to another origin.

## Varken parity

Varken wrote one InfluxDB measurement, `Radarr`, tagged by `type`. Every field it
recorded is covered:

| Varken field | Metricule metric | Notes |
|---|---|---|
| `type=Queue` `count` | `radarr_queue_items` | From `/queue/status`, so no pagination |
| per-item `hash` + `tmdbId`/`name`/`quality`/`protocol`/`titleSlug` tags | `radarr_queue_item_size_bytes`, `radarr_queue_item_remaining_bytes` | Labels carry the identity; the value is real bytes instead of a hash |
| `type=Missing` `count`, `movie_count` | `radarr_wanted_missing_movies` | See below |

Beyond Varken, this config also exposes `radarr_queue_items_unknown`,
`radarr_queue_errored`, `radarr_queue_warned`, `radarr_wanted_cutoff_unmet_movies`,
`radarr_calendar_movie_has_file`, `radarr_health_issue`, `radarr_disk_free_bytes`, and
`radarr_disk_total_bytes`.

Varken counted missing movies by fetching the entire library from `/movie` and
filtering for `monitored && !hasFile` in Python. `/wanted/missing` answers the same
question server-side, so this config reads only `totalRecords` with `pageSize=1`
instead of transferring the whole library on every poll.

Varken's `hash` field existed only so InfluxDB would store one row per queue item.
Prometheus identifies a series by its labels, so the per-item metrics carry the
identity as labels and use the download size as the value.

## Known limitations

**`count` and `movie_count` collapse into one metric.** Varken reported both, but
they were the same number computed twice — a total and the length of the filtered
list. `radarr_wanted_missing_movies` is that number.

**No relative date windows.** Metricule's `request.params` are static strings with no
templating, so a `start`/`end` range cannot be computed at collection time.
`radarr_calendar_movie_has_file` therefore uses Radarr's default calendar window,
which is today only.

**No per-movie library stats.** `/movie` returns the whole library — tens of megabytes
on a large instance — and Metricule would re-fetch it every interval. The aggregate
questions are answered more cheaply by `radarr_wanted_missing_movies` and
`radarr_wanted_cutoff_unmet_movies`. If you want per-movie `sizeOnDisk`, add a metric
on `/api/v3/movie` with `rows: "$[*]"`, raise `max_response_bytes`, and use a long
interval.

**Queue detail is capped.** `pageSize` is 200 and `max_series_per_metric` is 200 per
per-item metric. A deeper queue is truncated; the totals from `/queue/status` stay
correct because they are read separately. Metricule issues one request per
collection and does not paginate.

**Unknown-movie items are excluded from per-item metrics.**
`includeUnknownMovieItems` is `false` because a label selector must resolve on every
row: an item with `"movie": null` would fail the whole collection, since `null: omit`
governs the value and not labels. `radarr_queue_items_unknown` counts them instead.

**Label cardinality follows the queue.** `release`, `movie`, and `title_slug` are
unbounded in principle. If your queue is large, drop the two `radarr_queue_item_*`
metrics and alert on the totals.
