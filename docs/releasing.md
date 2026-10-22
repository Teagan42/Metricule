# Branching and releases

Versioning is automated. Nobody edits a version number, writes a changelog entry,
or pushes a tag by hand.

## Branching model

`main` is the only long-lived branch and is always releasable. Work happens on
short-lived branches named `<type>/<slug>`, matching the commit type: `feat/`,
`fix/`, `docs/`, `build/`, `ci/`, `refactor/`, `chore/`.

`main` is governed by two rulesets:

| Ruleset | Target | Effect |
|---|---|---|
| `main` | default branch | Pull request required, linear history, no force push, no deletion, squash-only merge, required status checks |
| `release tags` | `refs/tags/v*` | Release tags cannot be deleted or moved |

Required checks: `Validate PR title`, `Format, vet, lint`, `Unit tests`,
`Race detector`, `Validate example configurations`.

`Vulnerability scan` is deliberately **not** required. `govulncheck` tracks an
upstream database, so a newly published advisory would otherwise block every
unrelated pull request. Treat a failure as a release blocker, not a merge blocker.

Approvals are set to zero because the repository has a single maintainer. Raise
`required_approving_review_count` once a second reviewer exists.

## Commit and title convention

Conventional Commits, enforced on the pull request title by
`.github/workflows/pr-title.yaml`.

```text
feat(postgres): add listen/notify invalidation
fix(exporter): drop stale series after a failed refresh
```

Scopes follow the ownership table in `AGENTS.md`: `config`, `cli`, `schemas`,
`collect`, `snapshot`, `mapping`, `exporter`, `postgres`, `sqlite`, `httpjson`,
`triggers`, `source`, `telemetry`, `secrets`, `selector`, `runtime`.

Merges are squashed with the title as the commit subject and the pull request
body as the commit body. A breaking change needs either `!` after the scope
(`feat(config)!: ...`) or a `BREAKING CHANGE:` footer at the end of the body.

While the version is below `1.0.0`, `bump-minor-pre-major` is set: a breaking
change bumps the minor version, not the major.

## Release flow

1. A pull request merges to `main`.
2. `release-please.yaml` opens or updates a release pull request titled
   `chore(release): <version>`. It holds the computed version and the generated
   `CHANGELOG.md`.
3. Merging that pull request tags `v<version>`, creates the GitHub release, and
   writes `.release-please-manifest.json`.
4. The same workflow then calls `release.yaml`, which builds the binaries and the
   multi-architecture image, generates the SBOM, and attaches everything to the
   release.

`release.yaml` is a reusable workflow, not a tag-triggered one. A release created
with `GITHUB_TOKEN` fires no `release` event, so the publish step is invoked
directly instead of listening for one.

The first release will be `0.1.0`: the manifest starts at `0.0.0` and the existing
history contains `feat` commits.

## Required repository secret

`RELEASE_PLEASE_TOKEN` — a fine-grained personal access token scoped to this
repository with **Contents: read and write** and **Pull requests: read and
write**.

It exists for one reason: a pull request opened with `GITHUB_TOKEN` cannot trigger
workflow runs, so the release pull request would never report the status checks
the `main` ruleset requires, and would never become mergeable.

```sh
gh secret set RELEASE_PLEASE_TOKEN --repo Teagan42/Metricule
```

The workflow falls back to `GITHUB_TOKEN` when the secret is absent. In that mode
release pull requests must be merged by an administrator through the ruleset
bypass, or the required checks must be relaxed.

## Manual republish

To rebuild artifacts for a release that already exists:

```sh
gh workflow run release.yaml -f tag=v0.1.0
```

Assets are uploaded with `--clobber`, so the operation is idempotent.

## Inspecting or changing the rules

```sh
gh api repos/Teagan42/Metricule/rulesets
gh api repos/Teagan42/Metricule/rulesets/<id>
gh api -X DELETE repos/Teagan42/Metricule/rulesets/<id>
```
