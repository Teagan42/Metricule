# ADR 0011: Hot configuration reload by generation swap

## Status

Accepted for M5.

Date: 2026-10-28

## Context

Changing a metric interval or adding a source required restarting Metricule. A restart
drops the listener, closes pools, and reopens them; in orchestrated environments it
also causes a scrape gap. Reloading in place must not serve mixed state, must not lose
a working configuration when the new one is invalid, and must not mutate through an
unauthenticated surface.

## Decision

**Reload builds a new generation and swaps it in.** A generation owns its resolved
sources, compiled plans, scheduler, snapshot store, watch groups, and trigger targets.
`Runtime.Reload` validates, compiles, and opens the new generation before stopping the
old one. Any failure leaves the running generation untouched.

**Publication is swapped atomically.** The exporter reads a `storeSlot` holding an
atomic pointer to the current `MemoryStore`. Reload starts the new scheduler (bound to
its own store), swaps the slot, then drains the old generation. The old generation can
never publish into the new store because its scheduler holds a direct pointer to its
own store.

**The listener is immutable.** `server.listen` and endpoint paths are compared on
reload and a change is rejected. Rebinding would drop scrapes and complicate graceful
shutdown; changing those fields requires a restart.

**Reload is signalled, not exposed over HTTP.** `SIGHUP` is the trigger. Access to the
process or container is the authorization boundary. The exporter has no
authentication surface, so an HTTP reload endpoint would be an unauthenticated
mutation path.

**Shutdown cleanup does not run on reload.** Managed triggers are reconciled by
applying the new configuration, not deleted and recreated. `cleanup_on_shutdown`
applies only to process exit. `manage: false` means "stop managing", not "remove".

## Consequences

- Scrapes stay available across a reload; the application series set is empty until
  each new plan completes its first collection. Readiness may return `503` for that
  window by design.
- Removed sources lose their `metricule_source_up` series. Historical counters remain,
  because counters are not retracted.
- Reload serializes with startup and shutdown through one lifecycle lock. A reload
  cannot race the first generation into a half-started state.
- The whole document is reloaded; there is no partial patch operation.
- Server changes, and any future listener-level setting, require a restart.

## Alternatives considered

- **Restart the process via a supervisor.** Simplest, but drops the listener and all
  connections for every metric change.
- **Rebind the listener in place.** Could support server changes, but creates a scrape
  gap and a window where two listeners or none exist.
- **Mutate the existing scheduler and store in place.** Avoids a generation object but
  makes partial state observable and rollback after a late failure impossible.
- **HTTP reload endpoint.** Ergonomic, but introduces authentication and an externally
  reachable mutation path the project deliberately does not have.
