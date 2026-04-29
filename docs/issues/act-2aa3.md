---
title: "act version command"
deps: [act-9cad]
acceptance_criteria:
  - "`act version` prints the binary version, writer version, schema version, and (with `--check-repo`) the repo's max writer version."
  - "`--check-repo` walks all op files to find `max(writer_version)` and exits 4 if `self.writer_version < repo_max_writer_version`."
  - "Without `--check-repo`, `act version` works outside a `.act/` repo and exits 0."
  - "JSON output: `{\"ok\": true, \"binary_version\": \"<v>\", \"writer_version\": \"<v>\", \"schema_version\": <int>, \"repo_max_writer_version\": \"<v>\"|null}`."
  - "`--check-repo` exits 3 if `.act/` is missing; without `--check-repo`, missing `.act/` is not an error."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# act version command

## Context
Spec §3 `act version` (lines 826–840). Reports binary metadata; with `--check-repo`, doubles as a writer-version skew probe by scanning the op tree.

## Scope
- Parse flags `--check-repo`, `--json`.
- Always emit binary_version, writer_version, schema_version.
- With `--check-repo`: walk `.act/ops/**/*.json`, parse envelopes, compute `max(writer_version)`, compare to `self.writer_version`.

## Out of scope
- Mutation.
- Hooks, commits, or fold computation.
- Self-update / upgrade actions.

## Implementation notes
- Flags:
  - `--check-repo` bool. Performs the skew scan.
  - `--json` bool.
- Exit codes:
  - `0` success.
  - `3` `.act/` missing — only when `--check-repo` is set.
  - `4` `self.writer_version < repo_max_writer_version` — only when `--check-repo` is set.
- JSON schema: `{"ok": true, "binary_version": "<semver>", "writer_version": "<semver>", "schema_version": <int>, "repo_max_writer_version": "<semver>"|null}`.
- Without `--check-repo`, `repo_max_writer_version` is `null` and the command works anywhere (no repo required).
- `binary_version` is the build-time version of the act binary (e.g., from `runtime/debug.ReadBuildInfo` or LD-flags injected `version`). `writer_version` is the per-binary protocol version that ops are stamped with.
- `schema_version` matches what `act init` writes into `config.json`.
- Side effects: read-only.
- Walk strategy under `--check-repo`: traverse `.act/ops/` directly (no fold required); parse each envelope's `writer_version` field; track running max. Skip files that fail to parse with a stderr warning but continue.

## Test plan
- Plain `act version`: prints all four fields, `repo_max_writer_version: null`, exit 0.
- Plain `act version` outside any git repo: exit 0 (no env requirement).
- `act version --check-repo` in a repo with all ops at `0.1.0` and self at `0.1.0`: exit 0, `repo_max_writer_version: "0.1.0"`.
- `act version --check-repo` in a repo with one op at `0.2.0` and self at `0.1.0`: exit 4.
- `act version --check-repo` in a repo with all ops at older versions and self at `0.2.0`: exit 0, `repo_max_writer_version` reflects the older max.
- `act version --check-repo` outside `.act/`: exit 3.
- `act version --json`: JSON parseable, all keys present.
- `--check-repo` with empty `.act/ops/`: `repo_max_writer_version: null`, exit 0.
- Corrupt op file under `--check-repo`: stderr warning, scan continues, exit reflects only the version comparison.
- Verify `binary_version` and `writer_version` may differ (e.g., binary `0.1.0+abc`, writer `0.1.0`).
