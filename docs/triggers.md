# Managed PostgreSQL notification triggers

Metricule can install the database-side half of `mode: notify`: a trigger on each
watched table that calls `pg_notify` when the table changes. This is **opt-in** and
off by default (AD-006). Nothing is created unless a PostgreSQL source sets
`triggers.manage: true`.

Triggers are an optimization, not a requirement. An application can publish its own
`NOTIFY`; Metricule's listener does not care who sent it. When managed triggers are
absent or fail to install, collection still works and plans reconcile on
`fallback_interval` (§9.5).

## Configuration

```yaml
sources:
  appdb:
    type: postgres
    dsn:
      env: DATABASE_URL
    triggers:
      manage: true
      schema: metricule
      cleanup_on_shutdown: false
      # Optional: provisioning credentials with schema-owner privileges.
      dsn:
        env: DATABASE_ADMIN_URL

metrics:
  - name: app_jobs_pending
    type: gauge
    source: appdb
    query:
      sql: SELECT COUNT(*) AS value FROM jobs
    mapping:
      value: value
    collect:
      mode: notify
      channel: metricule_jobs_changed
      watch:
        tables:
          - public.jobs
```

A table and channel pair comes from a change-driven metric's `collect.channel` and
`collect.watch.tables`. Two metrics that share a table and channel produce one
trigger with the union of their `watch.events`. A metric with a channel but no
`watch.tables` cannot be turned into a table trigger; provisioning reports a warning
and continues.

Tables must be schema-qualified with plain identifiers, such as `public.jobs`.
Notification payloads are already qualified that way, so the listener can scope an
event to the plans that declared the table.

## What gets created

For each source with triggers enabled:

| Object | Name | Purpose |
|---|---|---|
| Schema | `triggers.schema` | Holds the function. Created when absent. Left in place on removal. |
| Function | `<schema>.metricule_notify_change()` | Sends `pg_notify(TG_ARGV[0], TG_TABLE_SCHEMA || '.' || TG_TABLE_NAME)`. |
| Trigger | `metricule_notify_<hash>` on each table | `AFTER INSERT OR UPDATE OR DELETE ... FOR EACH STATEMENT`, passing the channel as an argument. |

The function is shared by every table and channel; the channel is a trigger
argument. Trigger names are a stable hash of table and channel, stay inside
PostgreSQL's 63-byte identifier limit, and are identical across restarts and
instances.

Statement-level triggers are deliberate: a bulk write produces one notification, and
the trigger never reads or aggregates rows (§9.4).

## Lifecycle

`metricule serve` applies managed triggers once at startup, before collection starts,
and only when `triggers.manage: true`. Apply is idempotent: an object that already
matches produces no DDL. A restart does not create trigger churn.

Shutdown does **not** remove triggers by default. A sidecar that restarts should not
drop and recreate its notification objects. Set `cleanup_on_shutdown: true` to remove
them on a graceful exit. A killed process leaves them in place.

Explicit maintenance commands operate on the configuration's sources:

```console
$ metricule triggers plan   --config metricule.yaml
$ metricule triggers apply  --config metricule.yaml
$ metricule triggers status --config metricule.yaml
$ metricule triggers remove --config metricule.yaml
```

`--source appdb` limits any command to one source. `plan` prints the SQL that would
run and changes nothing. `apply` executes it in one transaction; a failure rolls the
whole source's provisioning back. `status` reports the function and every desired
trigger as `present`, `missing`, `changed`, or `foreign`, plus `orphan` triggers that
are managed but no longer desired. `remove` drops managed triggers, then the shared
function, and leaves the schema and every unrelated object alone.

Apply converges in both directions: a managed trigger that is no longer described by
the configuration (a removed metric, a changed channel) is dropped. The function is
shared and stays.

`plan` and `apply` require `triggers.manage: true`. `status` and `remove` work with
the block present and management off, so disabling provisioning does not strand
objects that were installed earlier.

## Ownership and safety

Every generated function and trigger carries a comment marker:

```text
metricule:owned:v1
metricule:owned:v1 hash=<subscription-hash>
```

Apply and remove act only on objects with that marker. If an object occupies a
generated name but is not marked:

- `apply` fails with an ownership error instead of overwriting it.
- `remove` leaves it in place.

The subscription hash covers table, channel, and trigger events. Changing any of
them makes apply drop and recreate that one trigger; an unchanged definition is left
untouched. `remove` only touches triggers attached to the managed function, so
Metricule never removes a user trigger or function.

Apply never drops the managed schema, even when it becomes empty. Removing a schema
is a destructive operation outside the scope of trigger management.

## Privileges

Provisioning is DDL and should not run as the collection role. Use `triggers.dsn`
with a role that can:

- `CREATE` on the managed schema, or `CREATE` on the database when the schema does
  not exist yet (Metricule creates it).
- The `TRIGGER` privilege on each watched table.
- `CREATE` on the managed schema for the function, plus ownership to comment on it.
- Read the PostgreSQL catalogs, which all roles can.

Example:

```sql
CREATE ROLE metricule_provisioner LOGIN PASSWORD '...';
GRANT CREATE ON DATABASE app TO metricule_provisioner;
GRANT CREATE ON SCHEMA metricule TO metricule_provisioner;
GRANT TRIGGER ON public.jobs TO metricule_provisioner;

-- The collection role stays read-only.
GRANT SELECT ON public.jobs TO metricule_reader;
```

Omitting `triggers.dsn` falls back to the collection DSN and validation emits a
warning: a role that should only read is then asked to create objects. Granting it
DDL rights weakens the read-only guarantee (§14.2).

## Failure behavior

| Failure | Behavior |
|---|---|
| Provisioning credentials rejected or lack privileges | `serve` logs the error and continues; collection and fallback reconciliation are unaffected |
| Watched table missing at apply time | Apply fails for that source and rolls back; the error names the table |
| Foreign object occupies a generated name | Apply fails with an ownership error; nothing is overwritten |
| Trigger definition drifted from configuration | `apply` drops and recreates that trigger inside the transaction |
| One statement in a multi-object apply fails | The whole source's apply rolls back |
| Listener disconnects | Managed triggers keep notifying; the listener reconnects and reconciles (AD-005) |
| Process killed | Managed objects remain; the next startup's apply is a no-op if they match |

Trigger installation never happens on the collection connection. `internal/triggers`
opens its own short-lived connection with the provisioning DSN, so a failure cannot
corrupt the pool or an in-flight query.

## Overhead

Each statement that inserts, updates, or deletes on a watched table runs the trigger
function once. The function performs one `pg_notify` and returns. It does not query
the table, inspect rows, or aggregate.

Consequences worth knowing:

- `AFTER ... FOR EACH STATEMENT` fires even when a statement changed zero rows, for
  example `UPDATE jobs SET state = 'queued' WHERE false`. Expect a notification for
  the statement, not for the row count.
- A transaction batching many statements produces one notification per statement.
  The listener and scheduler coalesce bursts into a single pending refresh.
- `watch.events` selects the DML kinds at trigger creation. PostgreSQL cannot filter
  by changed column through a trigger argument, so an `UPDATE` on an unwatched column
  still notifies when update is enabled.

Measure the write cost on your own workload. If the table is extremely write-heavy
and the metric can tolerate polling, prefer `mode: poll`.

## Known limitations

- Only statement-level `INSERT`, `UPDATE`, and `DELETE` are supported. `TRUNCATE`
  cannot be combined with other events in one PostgreSQL trigger and is not managed.
- The payload is the qualified table name, never row data (§9.4).
- The managed schema is created but never removed.
- `cleanup_on_shutdown` only runs on graceful shutdown; SIGKILL or container
  termination leaves the objects in place.
- Provisioning is per source and per database. Two sources pointing at the same
  database with different `triggers.schema` values create two independent sets of
  objects.
- Removing the function while a user trigger depends on it is refused unless the
  dependency is one of Metricule's own. The function is then retained.
