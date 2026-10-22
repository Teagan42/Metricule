# ADR 0005: Capability declaration is separate from adapter registration

Status: Accepted
Date: 2026-10-21

## Context

§6.2 requires an explicit capability descriptor, and unsupported requested
capabilities must produce actionable validation errors. §5.5 lists "unsupported
features for an adapter" among the checks offline validation must perform, and §15.1
requires offline validation to connect to nothing.

The obvious design has each adapter register its own capabilities in an `init`
function. That creates two problems. Offline validation would depend on the adapter
being linked, and during parallel development — where M0 defines contracts and M1
implements adapters — `metricule validate` could not check collection modes at all.

## Decision

Split the registry into two operations:

- `source.DeclareCapabilities(type, caps)` — what a type supports. Declared in
  `internal/source/capabilities.go`, owned by Agent 0.
- `source.Register(type, factory)` — how to construct it. Called by each adapter.

`Register` panics if the type has no declared capabilities. `New` distinguishes a
type that is declared but unimplemented from one that is unknown.

## Consequences

Capabilities become part of the configuration contract rather than an implementation
detail, which is what they already were: AD-008 fixes the v0.1 adapter set, and
whether PostgreSQL supports `LISTEN/NOTIFY` is a fact about PostgreSQL, not a property
of our code.

`metricule validate` works completely with no adapter linked. A metric asking for
`mode: notify` on a SQLite source is rejected with a pointer to `mode: auto`, which is
the error a user actually needs.

Error messages distinguish the two failure modes honestly. "adapter for type
\"sqlite\" is declared but not implemented in this build" tells a user their config is
valid and their binary is incomplete — materially different from "unsupported type".

The cost is that the table can lie. An adapter declaring `Notifications: true` while
implementing nothing would pass validation and fail at runtime. `AGENTS.md` rule 3
covers this: an adapter that cannot deliver a declared capability is a defect in the
adapter, not a reason to weaken the table.
