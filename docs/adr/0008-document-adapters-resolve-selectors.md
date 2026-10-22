# ADR 0008: Document adapters resolve selectors, not the mapping engine

Status: Accepted
Date: 2026-10-22

## Context

The specification is genuinely contradictory about who evaluates JSON selectors.

- §11.1 lists "JSON selectors" among the HTTP adapter's required features, and §18
  assigns "Selector evaluation" to Agent 6.
- §12.2 lists "JSON selector extraction" among the mapping engine's supported
  conversions.

Agent 6 surfaced this during M1: `source.QueryDefinition` carried no selector fields,
so the HTTP adapter could not perform the row extraction its brief assigned, while the
selectors themselves live in `config.Mapping`, which reaches only the scheduler and the
mapping engine.

The alternative considered was to have the HTTP adapter return a single-row
`ResultSet` holding the decoded JSON document in one column, leaving the mapping engine
to apply `rows`, `value`, and `labels`.

## Decision

Document-oriented adapters resolve selectors and emit a normalized `ResultSet`.
`QueryDefinition` gains `Rows`, `Value`, and `Labels`.

**Resolved columns are keyed by the selector string itself.** `row[mapping.Value]`
therefore resolves identically whether `Value` is a SQL column name or a JSONPath
expression.

`source.Row` keeps `map[string]any`. An absent key means the path matched nothing; a
present `nil` means the document held null. The two-value map lookup distinguishes
them, so no sentinel type is added to the shared package.

Division of responsibility:

| Concern | Owner |
|---|---|
| Selector compilation and evaluation | Adapter |
| Row extraction and `MaxRows` enforcement | Adapter |
| Non-finite number rejection | Adapter |
| Absent vs. null signalling | Adapter |
| Null policy, scaling, state-set expansion | Mapping |
| Cardinality limits, duplicate labelsets, label sorting | Mapping |

## Consequences

Three constraints outrank §12.2's wording and decide this:

1. **P7** states the metric mapper must contain no source-specific implementation
   logic. The blob-column alternative requires the mapper to recognise that a
   particular column holds a JSON document and switch to selector evaluation — which
   is exactly source-specific logic, and would recur for every future document source.
2. **§12** opens by requiring the mapping engine to operate on *normalized* result
   sets. A single column containing an undecoded document is not normalized.
3. **`QueryRequest.MaxRows` already specifies** that adapters must error rather than
   truncate (§8.7). For an HTTP source, rows do not exist until the `rows` selector is
   evaluated, so the adapter must evaluate it to honour a contract already written
   down. The alternative would have moved `MaxRows` enforcement into the mapper,
   contradicting the documented interface.

Keying columns by selector text is the part that makes this work. Renaming extracted
fields to canonical names such as `value` would reintroduce the branch: the mapper
would need to use configured names for SQL and canonical names for HTTP.

The cost is that `QueryDefinition` now carries fields only some adapters read, so the
struct is a union rather than a tight interface. That was already true of `SQL` versus
`Method`/`Path`, and adapters are required to reject fields they do not support.

§12.2 is reinterpreted, not overridden: the mapping engine still *consumes* selector
results and owns every conversion listed there apart from the extraction step itself.
