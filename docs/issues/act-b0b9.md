---
title: "act init command"
deps: [act-1396, act-9cae]
acceptance_criteria:
  - "Running `act init` inside a git working tree creates `.act/{ops,snapshots,hooks,imports}/`, an empty `index.db`, and a `config.json` with a freshly generated 8-hex `node_id`, `schema_version`, `writer_version`, and an empty `fold-checkpoint` placeholder."
  - "`act init` outside a git working tree (including bare repos) exits 3."
  - "`act init` against an existing `.act/` without `--force` exits 2; with `--force` it merges missing subdirs and rewrites `config.json` only if absent, never touching existing ops or snapshots."
  - "JSON output always emitted: `{\"ok\": true, \"act_dir\": \"/abs/path/.act\", \"node_id\": \"<8hex>\", \"writer_version\": \"<v>\", \"schema_version\": <int>}`."
  - "init does NOT create a git commit; the first real op-commit is deferred to a subsequent write."
status: open
created_at: 2026-04-29T00:00:00Z
---

# act init command

## Context
Spec §3 `act init` (lines 535–554). Bootstraps the `.act/` directory tree, generates a stable `node_id`, and seeds `config.json`. References §1.6 for on-disk layout and §2.1 for HLC `node_id` derivation.

## Scope
- Parse and validate the single optional flag `--force`.
- Detect the enclosing git working tree (nearest `.git`); resolve submodule vs superproject scope.
- Create directory tree `.act/ops/`, `.act/snapshots/`, `.act/hooks/`, `.act/imports/`.
- Compute `node_id = sha256(machine-id || git-config user.email)[0:8]`.
- Write `.act/config.json` with `{node_id, schema_version, writer_version, fold_checkpoint: null}`.
- Initialize empty SQLite `.act/index.db` with the index schema (delegated to act-912f's installer entrypoint).
- Always emit JSON output to stdout.

## Out of scope
- Hook installation beyond creating the empty `.act/hooks/` directory.
- Any git commit (deferred to first real op).
- Network or remote configuration.
- Index population or rebuild (the db is created empty; first fold populates).

## Implementation notes
- Flags: `--force` (bool, default false). No universal write-flags apply except by design — `init` writes no op file.
- Exit codes:
  - `0` success.
  - `2` `.act/` already exists and `--force` was not provided.
  - `3` `cwd` is not inside a git working tree (bare repo or outside any `.git`).
- JSON schema (always emitted): `{"ok": true, "act_dir": <abs path>, "node_id": <8 hex>, "writer_version": <semver>, "schema_version": <int>}`.
- Side effects: creates dirs and files under `.act/`. No git commit, no staging.
- `--force` semantics: idempotent restoration. Missing subdirs are created. `config.json` is only rewritten if currently absent. Existing ops/snapshots are never overwritten or moved.
- `node_id` derivation MUST be deterministic for a given (machine-id, email) pair so re-init on the same machine reproduces the id only when machine-id+email match.

## Test plan
- Fresh empty git repo: assert all four subdirs exist, `config.json` is valid JSON with expected keys, `index.db` exists and is openable as SQLite, exit 0.
- Bare git repo: exit 3, no files created.
- Outside any git repo: exit 3.
- Re-run on a populated `.act/` without `--force`: exit 2, no mutation.
- Re-run with `--force` after deleting `.act/snapshots/`: snapshots dir restored, ops untouched, `config.json` unchanged, exit 0.
- Re-run with `--force` after deleting `.act/config.json`: config.json regenerated with the same `node_id` (deterministic), exit 0.
- Submodule: `.act/` is created at the submodule root, not the superproject.
- Verify JSON output stable-keyed and parseable; `node_id` matches `^[0-9a-f]{8}$`.
