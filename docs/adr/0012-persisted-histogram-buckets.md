# ADR 0012: Histograms from persisted cumulative buckets

## Status

Accepted for M5.

Date: 2026-10-28

## Context

§8.5 deferred histograms until their semantics could be verified. Two shapes were
possible: derive buckets from raw observations, or expose buckets the application
already persists. Metricule has no event stream and no durable observation history, so
deriving buckets would mean inventing data. Persisted cumulative buckets already exist
in many applications and can be read with the same normalized result contract.

## Decision

**Metricule exposes buckets; it does not build them.** A `type: histogram` metric reads
already-cumulative bucket counts plus a total count and sum.

The mapping names four fields:

- `value`: the cumulative bucket count
- `buckets`: the bucket bound (`le`), numeric or `+Inf`
- `count`: the total observation count
- `sum`: the total observation sum

Validation is strict:

- `+Inf` is required and its count must equal `count`.
- Bucket counts must be non-decreasing as the bound grows.
- `count` and `sum` must be constant for every row sharing a labelset.
- Duplicate bounds fail the collection.
- Summaries remain unsupported.

`scale` multiplies bucket bounds and the sum, never counts: unit conversion changes the
axis, not how many observations there were. Series limits count `buckets + 2` because
exposition generates `_sum` and `_count` alongside `_bucket`.

The snapshot model gains `Family.Histograms` with a `Histogram` value: sorted labels,
cumulative buckets, count, and sum. The exporter emits it with
`prometheus.NewConstHistogram`, so `_bucket`, `_sum`, `_count`, and the `histogram` type
are produced by the client library rather than hand-written text.

## Consequences

- A source that stores per-bucket deltas must convert them in SQL. Metricule will not
  guess whether a count is cumulative.
- A missing `+Inf` bucket fails the collection instead of being synthesized from
  `count`; synthesizing would hide a broken source query.
- `_count` joins `_bucket` and `_sum` as a reserved metric-name suffix.
- Histogram series count against cardinality limits as their generated series, not as
  one series per labelset.
- Buckets may be negative or fractional; only NaN, `-Inf`, duplicate bounds, and
  decreasing counts are rejected.
- Native Prometheus histograms are not supported; this is the classic cumulative
  bucket representation.

## Alternatives considered

- **Derive buckets from raw rows.** Requires an event stream and windowing state that
  Metricule deliberately does not keep.
- **Accept raw per-bucket deltas and cumulate them.** Convenient, but silently changes
  the source's meaning and makes mixed cumulative/non-cumulative data indistinguishable.
- **Synthesize `+Inf` from `count`.** Hides a query that forgot the final bucket and
  makes the resulting distribution wrong.
- **Expose `_bucket` series as gauges.** Avoids a snapshot model change but loses the
  `histogram` type and `histogram_quantile` compatibility.
