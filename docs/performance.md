# Performance

Measured results for §20.6. These are reference numbers, not gates: hardware and
kernel differ, and CI does not run benchmarks. Re-run the commands below on the
hardware you plan to deploy.

## Method

| Measurement | Command |
|---|---|
| Scrape, publication, current read, memory | `make bench` |
| Notification latency, burst coalescing | `make bench` (`internal/collect`) |
| PostgreSQL trigger write overhead | `make bench-integration` |
| Process startup to `/readyz` | `make bench-integration` (`tests/integration/sqlite`) |
| Idle CPU and resident memory | `make bench-idle` |

`make bench` runs Go benchmarks with `-benchtime=1s`. `make bench-integration`
requires Docker and uses `-benchtime=2s`. The startup benchmark launches the real
binary; the idle script starts the real binary against a temporary SQLite database
and samples cumulative CPU time.

## Environment

- Apple M4, 10 cores, macOS
- Go 1.27, `CGO_ENABLED=0`
- PostgreSQL 17 (`postgres:17-alpine`) in Docker for trigger benchmarks
- Collected 2026-10-27

Absolute numbers will differ. Ratios, allocation shapes, and the absence of I/O on
the scrape path are the durable results.

## Scrape latency

Handler exercised directly, without a socket. Body is Prometheus text exposition.

| Published series | Time per scrape | Body | Allocations |
|---:|---:|---:|---:|
| 1,000 | 0.59 ms | 28 KB | 0.8 MB |
| 10,000 | 6.39 ms | 289 KB | 8.2 MB |
| 100,000 | 66.7 ms | 3.08 MB | 81 MB |

Each scrape reads the immutable snapshot and performs no SQL, HTTP, or filesystem
work (P2). Latency and allocation scale linearly with series count.

The allocation figure is dominated by building one Prometheus descriptor per sample
per scrape. It is transient garbage, not retained memory, but at 100,000 series a
scrape allocates ~81 MB. Keep cardinality bounded and prefer scrape intervals that
match your series count. A future release could cache descriptors per family and
labelset.

## Snapshot store

| Series | Publish (full replacement) | Current read | Retained heap |
|---:|---:|---:|---:|
| 1,000 | 43 µs | 55 ns | 68.2 B/series |
| 10,000 | 504 µs | 58 ns | 68.7 B/series |
| 100,000 | 5.58 ms | 58 ns | 69.2 B/series |

`Current` is constant-time in series count because it copies snapshot pointers, not
samples. A full publication is linear: the store validates the result and swaps one
pointer. Retained memory is dominated by the sample, one label pair, and slice
headers, and it grows predictably with configured limits.

## Notification to publication

`mode: notify`, zero debounce, one plan, one store, measured from
`Scheduler.Invalidate` to a successful `Store.Publish`.

| Percentile | Latency |
|---|---:|
| p50 | 4.6 µs |
| p95 | 8.4 µs |
| p99 | 12.5 µs |
| max (500 iterations) | 52.7 µs |

The benchmark average is ~2.7 µs; the percentile test includes scheduler wakeup and
store publication. With a configured `debounce`, add that window: a trailing
debounce deliberately waits before refreshing.

## Burst coalescing

1,000 notifications sent while one collection is in flight, with a 5 ms debounce:

```text
notifications/publication: 1000
```

The burst produces one follow-up publication, not one thousand. The scheduler holds
one pending invalidation and drops the rest, which is what bounds work under a write
storm (rule 10).

## PostgreSQL managed trigger overhead

One statement per iteration against identical tables, one with a managed trigger and
one without. Includes the client round trip.

| Workload | Without trigger | With trigger | Overhead |
|---|---:|---:|---:|
| `INSERT` one row | 121.5 µs | 145.8 µs | +24.2 µs (+20%) |
| `INSERT ... generate_series(1,100)` | 187.1 µs | 287.1 µs | +100 µs (+53%, ~1 µs/row) |

A statement-level trigger fires once per statement and calls `pg_notify`, so the
per-row share falls as the statement grows. For an extremely write-heavy table, weigh
this against polling (see [`triggers.md`](triggers.md#overhead)).

## Startup

Real binary, SQLite source, one poll metric, measured from process launch to a `200`
from `/readyz`.

| Runs | Average startup |
|---:|---:|
| 54 | 18.3 ms |

A cold first launch measured ~700 ms on the same machine; the difference is OS page
cache and Go runtime warmup. Startup covers config load, source open, trigger
provisioning (none in this configuration), scheduler start, listener bind, and the
first collection attempt.

## Idle cost

One SQLite source, one metric polling hourly, no scrapes, 30-second window:

```text
cpu percent: 0.000%
rss: 19.1 MiB
```

Cumulative CPU advanced by less than the timer resolution over 30 seconds. The health
loop probes every 15 seconds; with a local source that is not measurable here. Idle
work is dominated by that probe and by scrape handling, not by collection scheduling.
