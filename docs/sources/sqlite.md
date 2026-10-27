# SQLite source adapter

Read-only collection from a database file owned by the monitored application
(§10, AD-007). Adapter-scoped notes; `docs/configuration.md` remains the
configuration reference once it lands.

## Configuration

```yaml
sources:
  app:
    type: sqlite
    path: /data/app.db
    readonly: true              # must be true in v0.1 (AD-007)
    busy_timeout: 3s            # lock wait before a collection fails
    change_check_interval: 2s   # how often Watch reads PRAGMA data_version
```

`path` accepts a relative path, resolved against the process working directory
at startup.

## Read-only guarantee

Connections are opened as `file:<path>?mode=ro&_query_only=1`. SQLite rejects
every write with `SQLITE_READONLY` and will not create a missing file. `Open`
also reads back `PRAGMA query_only` and refuses to collect on a writable
connection.

The adapter never alters the journal mode and installs no trigger. The
guarantee is enforced by the connection, not by inspecting statements: §14.5
states that SQL cannot be proven read-only by string matching. Grant the
exporter read-only filesystem access and keep the SQL in configuration under the
same review as any other code.

`tests/integration/sqlite` asserts the guarantee by content hash and
modification time across a full collection cycle.

## Filesystem permissions

The published image runs as uid 65532. The database file and every parent
directory must be readable by that uid. A permission failure names the uid.

WAL-mode databases need more than the main file:

- `<path>-wal` must be readable.
- SQLite creates a `<path>-shm` index beside the database on first read, even
  from a read-only connection, because WAL readers coordinate through shared
  memory. The directory must therefore permit creating that file, or a `-shm`
  must already exist from the application's own connection.
- A WAL database closed cleanly, with no `-wal` left behind, is readable with no
  directory write access at all.

A fully read-only bind mount is therefore safe for rollback-journal databases
and for cleanly closed WAL databases, but a live WAL database generally needs a
writable directory. Mount the data directory read-write and rely on `mode=ro`,
or pre-create the sidecars.

## Failure behavior

| Situation | Behavior |
|---|---|
| File absent | Collection and `Health` fail; both recover when it returns |
| File replaced | Detected by inode identity, pool reopened on the next call |
| Database locked | Waits up to `busy_timeout`, then fails the collection |
| Result exceeds `max_rows` | Collection fails; results are never truncated (§8.7) |
| Duplicate result column names | Collection fails; alias the column |
| Context cancelled or deadline reached | Statement is interrupted |

Collections never return a partial `ResultSet`. A failure is visible rather than
being reported as zero (P3, AD-010).

## Known limitations

- Change observation compares `PRAGMA data_version` on a dedicated read-only
  connection. It reports that the database changed, never which table (§10.2), so
  every `auto` plan on the source refreshes.
- A file replacement is detected by inode identity and re-establishes the observer.
  The replacement is announced as a reconnect so the scheduler reconciles.
- `SQLiteSettings` carries no query timeout. A collection context deadline is
  honored exactly; with no deadline, a 30s backstop bounds the statement.
- One connection pool per source, capped at four connections, plus one pinned
  observer connection. Not configurable.
