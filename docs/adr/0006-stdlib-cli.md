# ADR 0006: Standard library flag package instead of a CLI framework

Status: Accepted
Date: 2026-10-21

## Context

§15 specifies the command surface: `serve`, `validate` (with `--online`), `test`,
`config schema`, and `version`. §9.3 adds four `triggers` subcommands in M3.

§4.2 does not name a CLI library. §4.3 forbids a dependency that exists purely for
convenience where the standard library suffices.

## Decision

Use `flag` with a hand-written subcommand dispatch table in `cmd/metricule/main.go`.

Exit codes are explicit: 0 success, 1 runtime failure, 2 usage error.

## Consequences

Seven commands with a handful of flags each is within what `flag` handles cleanly.
Cobra would add roughly ten transitive modules for shell completion, help templating,
and nested command trees that this surface does not need.

What is given up: generated shell completions, `--help` subcommand discovery beyond
the hand-written usage text, and automatic suggestion of near-miss command names.
If M5 grows the command tree substantially, this record should be superseded rather
than worked around.

Separating exit code 2 from 1 is deliberate and load-bearing for deployment. A
container restart loop caused by a malformed flag looks identical to one caused by an
unreachable database unless the codes differ.

Dispatch takes explicit `stdout` and `stderr` writers rather than using the globals,
which is what makes the process-level tests in `cmd/metricule/main_test.go` able to
assert stream routing.
