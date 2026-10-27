# ADR 0010: Managed trigger ownership and shape

## Status

Accepted for M3.

Date: 2026-10-27

## Context

Metricule can watch PostgreSQL changes by listening for `NOTIFY`. The application
may send those notifications itself, or Metricule can install table triggers that
do. Installing objects in someone else's database is a mutation: it must be opt-in,
idempotent, and reversible without risking user objects (§9.3, AD-006).

The design must answer four questions:

1. How does Metricule know an object is its own?
2. How many functions and triggers does a database need?
3. When is a trigger recreated?
4. What happens when provisioning fails?

## Decision

**Ownership is a comment marker on each object.** Generated functions and triggers
carry `metricule:owned:v1`; triggers append ` hash=<subscription-hash>`. Apply and
remove act only on objects whose marker matches. An object that occupies a generated
name without the marker is an error: apply refuses to overwrite it, and remove
leaves it alone.

**One shared function per managed schema, one trigger per table and channel.**
`<schema>.metricule_notify_change()` takes the channel as a trigger argument and
sends `pg_notify(TG_ARGV[0], TG_TABLE_SCHEMA || '.' || TG_TABLE_NAME)`. Trigger names
are a stable hash of table and channel, so repeated applies and restarts find the
same object rather than creating new ones.

**Triggers are statement-level and DML-scoped.**
`AFTER INSERT OR UPDATE OR DELETE ... FOR EACH STATEMENT` produces one notification
per statement and never reads or aggregates rows. A bulk load does not cause one
notification per row.

**The subscription hash drives convergence.** It covers table, channel, and event
list. An unchanged definition produces no DDL. A changed definition drops and
recreates that trigger inside the transaction. The function is recreated only when
its marker version changes.

**Provisioning is explicit and non-fatal to collection.** `serve` applies at startup
only when `triggers.manage: true`, on a separate connection using the provisioning
DSN. A failure is logged; collection continues and `fallback_interval` repairs
missed changes. Shutdown removes objects only when `cleanup_on_shutdown: true`.

## Consequences

- No registry table is needed. Comments are queryable through `obj_description` and
  survive `pg_dump`/restore.
- A user can read the marker and know what wrote an object.
- Hash-based names are not human-readable, but they are stable and bounded. Status
  output maps them back to table and channel from configuration.
- A marker can be copied deliberately, but accidental collision with a user object
  is reported instead of destructive.
- The managed schema is created but never dropped. Removing schemas is out of scope.
- Two sources that point at one database with different managed schemas own separate
  object sets; this is permitted and visible in `status`.
- `TRUNCATE` is not managed because PostgreSQL cannot combine it with the other
  events in a single trigger.

## Alternatives considered

- **A registry table.** More explicit, but adds a second object to create and
  migrate, and a lost or restored registry could strand triggers. Comments keep
  ownership attached to the object.
- **One function per channel.** Avoids trigger arguments but creates many functions
  and amplifies the number of objects to own and remove.
- **Row-level triggers.** Simpler payloads, but one notification per row and more
  write overhead.
- **Always recreate on apply.** Simpler code, but it produces trigger churn on every
  restart, exactly what §9.3 warns against.
