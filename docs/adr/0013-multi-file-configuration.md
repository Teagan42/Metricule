# ADR 0013: Configuration split across files

Status: Accepted
Date: 2026-11-15

## Context

One exporter commonly serves several unrelated applications, such as Sonarr, Radarr,
and a database. A single file mixes their sources and metrics, so editing one service
means editing a shared file, and a deployment cannot add a service by dropping in a
file. Operators were left to concatenate files with external tools, where a duplicate
source name silently overwrites the earlier definition before Metricule ever sees it.

## Decision

**`--config` accepts files and directories and may be repeated.** A directory
contributes its top-level `*.yaml` and `*.yml` files in name order. Subdirectories and
hidden entries are ignored, so a backup folder is never loaded and a Kubernetes
ConfigMap mount, whose keys are symlinks into a hidden directory, loads each key once.

**Every file is a complete-looking document.** Each must declare `apiVersion` and
`kind`. This rejects stray YAML in a config directory and leaves room for files of
different versions later.

**Merging never picks a winner.** Sources merge by name, and a name declared twice is
an error. Metrics concatenate in load order. `metadata`, `server`, and `defaults` may
be declared in at most one file; a field-by-field merge would make the effective value
depend on file order, which violates fail-visibly (P3). All merge conflicts are
reported together.

**Validation runs once, on the merged document.** Cross-file references, such as a
metric naming a source in another file, validate normally. Presence tracking is
rebased onto merged metric indices so explicit zeros keep their meaning (§5.1), and
diagnostics are mapped back to `file: local-path`.

**A single file behaves exactly as before**, including unprefixed diagnostic paths.

## Consequences

- The JSON Schema describes one file, so it no longer requires `sources` or
  `metrics`. "At least one of each" moved from schema to Go validation only, which
  keeps ADR 0007's direction: the schema accepts everything Go accepts.
- Reload (ADR 0011) re-lists directories, so adding a file and sending `SIGHUP` adds
  its metrics. Reload remains whole-configuration.
- File order affects only metric order, which affects nothing observable at scrape.
- There is no recursive directory loading and no `include:` key. Both can be added
  later without changing the merge rules.

## Alternatives considered

- **An `include:` list in the main file.** Keeps one entry point, but adds a schema
  key, relative-path resolution, and cycle detection, and still needs a directory form
  for drop-in deployments.
- **Last file wins for duplicates.** Familiar from other tools, but silently discards
  configuration, which is the failure this change exists to prevent.
- **Header only in one file.** Less repetition, but any YAML file in the directory
  would be merged, and editors lose the schema anchor for split files.
