# Sonarr

Replaces [Varken](https://github.com/Boerderij/Varken)'s Sonarr collector with a
declarative Metricule configuration. No InfluxDB, and no Python service: Prometheus
scrapes Metricule, Metricule polls the Sonarr API.

## Run

```sh
export SONARR_API_KEY="$(grep -oP '(?<=<ApiKey>)[^<]+' /config/config.xml)"
metricule validate --config examples/sonarr/metricule.yaml
metricule serve --config examples/sonarr/metricule.yaml
```

The API key is in Sonarr under *Settings → General → API Key*, or in `config.xml`.
Point `base_url` at your instance and scrape `:9709/metrics`.

```yaml
scrape_configs:
  - job_name: sonarr
    static_configs:
      - targets: ["metricule-sonarr:9709"]
```

Check one metric without starting the server:

```sh
metricule test --config examples/sonarr/metricule.yaml --metric sonarr_queue_items
```

## Authentication

Sonarr accepts the API key only as an `X-Api-Key` header or an `apikey` query
parameter — it rejects `Authorization`. The config uses `type: api_key`, which keeps
the key in a secret reference:

```yaml
authentication:
  type: api_key
  header: X-Api-Key
  token:
    env: SONARR_API_KEY
```

Use `file:` instead of `env:` to read it from a mounted secret. Putting the key in the
source `headers` map would work but stores a credential in plain configuration; the
`apikey` query parameter would additionally put it in request URLs. Neither is
recommended.

`max_redirects` is 0 because a redirect would carry `X-Api-Key` to another origin.

## Varken parity

Varken wrote one InfluxDB measurement, `Sonarr`, tagged by `type`. Every field it
recorded is covered:

| Varken field | Metricule metric | Notes |
|---|---|---|
| `type=Queue` `count` | `sonarr_queue_items` | From `/queue/status`, so no pagination |
| per-item `hash` + `sonarrId`/`name`/`epname`/`sxe`/`protocol`/`quality` tags | `sonarr_queue_item_size_bytes`, `sonarr_queue_item_remaining_bytes` | Labels carry the identity; the value is real bytes instead of a hash |
| `type=Missing` `count`, `episode_count` | `sonarr_wanted_missing_episodes` | Server-side total, not a date window — see below |
| `type=Missing` `show_count`, `season_count` | not replicated | See below |
| per-episode calendar `hash` + `downloaded` tag | `sonarr_calendar_episode_has_file` | `downloaded` becomes the 0/1 value |

Beyond Varken, this config also exposes `sonarr_queue_items_unknown`,
`sonarr_queue_errored`, `sonarr_queue_warned`, `sonarr_wanted_cutoff_unmet_episodes`,
`sonarr_health_issue`, `sonarr_disk_free_bytes`, and `sonarr_disk_total_bytes`.

Varken's `hash` field existed only so InfluxDB would store one row per queue item.
Prometheus identifies a series by its labels, so the per-item metrics carry the
identity as labels and use the download size as the value.

## Known limitations

**No relative date windows.** Varken queried `/calendar` with
`start=<today - N days>&end=<today>`. Metricule's `request.params` are static strings
with no templating, so that window is not expressible. Two consequences:

- `sonarr_wanted_missing_episodes` counts *all* monitored missing episodes from
  `/wanted/missing`, not those that aired in the last N days. For alerting this is
  usually the better signal, but it is not the same number Varken reported.
- `sonarr_calendar_episode_has_file` uses Sonarr's default calendar window, which is
  today only.

**No `show_count` or `season_count`.** Varken derived these by grouping missing
episodes client-side. Metricule maps rows to series and does not aggregate, and
Sonarr exposes no endpoint that returns those counts. Grouping is a query-time
concern:

```promql
count(count by (series) (sonarr_queue_item_size_bytes))
```

**Queue detail is capped.** `pageSize` is 200 and `max_series_per_metric` is 200 per
per-item metric. A deeper queue is truncated; the totals from `/queue/status` stay
correct because they are read separately. Metricule issues one request per
collection and does not paginate.

**Unknown-series items are excluded from per-item metrics.**
`includeUnknownSeriesItems` is `false` because a label selector must resolve on every
row: an item with `"series": null` would fail the whole collection, since `null: omit`
governs the value and not labels. `sonarr_queue_items_unknown` counts them instead.

**Label cardinality follows the queue.** `release` and `series` are unbounded in
principle. If your queue is large, drop the two `sonarr_queue_item_*` metrics and
alert on the totals.
