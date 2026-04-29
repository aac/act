---
title: "Auto-commit and push policy"
deps: [act-6ec9, act-9824]
acceptance_criteria:
  - "After every write op (create, update_field, claim, close, dep add, etc.) the policy stages the new op file via `git add <op_path>` and runs `git commit --no-verify -m \"act-op: <id> <op_type>\"` unless --no-commit is set"
  - "When --push is given (or config.auto_push=true and no --no-commit), runs `git push` after the commit; exits non-zero on push failure with a structured error"
  - "Universal flag combinations enforced (per §4): --no-commit + --push → exit 2; --isolated + --push → exit 2"
  - "--verify (vs the default --no-verify) toggles the host git hooks during op-commit; default is no-verify because op commits touch only .act/ops/**"
  - "Default commit message format is exactly `act-op: <issue_id_short> <op_type>` where issue_id_short is the prefix of the issue id matching the shortest-unique-prefix at write time (or full 16 hex for new ids)"
  - "Reads `.act/config.json` for auto_commit and auto_push booleans; flags override config; --no-commit overrides auto_commit=true"
  - "On commit failure, the staged op file is unstaged via `git restore --staged <op_path>` and the op file is left on disk (writer surfaces the git error to caller; exit 8 with op_invalid)"
status: closed
created_at: 2026-04-29T00:00:00Z
---

# Auto-commit and push policy

## Context
Spec §4 ("Universal flags") and §2.7 (`.act/config.json`'s `auto_commit`/`auto_push`) define the post-write commit/push behavior. Every write command shares this policy; centralizing it here avoids per-command drift.

## Scope
- Implement `commit_op(op_path, issue_id, op_type, opts) -> CommitResult`:
  1. `git add <op_path>` (path must be under `.act/ops/**`).
  2. If `opts.no_commit`: stop, return `{committed: false, pushed: false}`.
  3. Run `post-<op_type>` hook (act-ce9f) — hook owns its own staging-revert path on failure.
  4. `git commit <flags> -m "act-op: <id_short> <op_type>"` where `flags = --no-verify` unless `opts.verify`.
  5. If `opts.push` or (`config.auto_push` and not `opts.no_commit`):
     - Reject if `opts.isolated`: exit 2.
     - Run `git push` against the configured remote/branch.
- Honor universal flag precedence rules verbatim from §4:
  - `--no-commit` + `--push` → exit 2 (`{"error": "usage", "message": "...cannot push uncommitted op"}`).
  - `--isolated` + `--push` → exit 2.
  - `--verify` + `--no-commit` → silent no-op (no commit happens, no hook).
- Read `auto_commit` and `auto_push` from `.act/config.json`. Flag values override config. Effective decision: `should_commit = !opts.no_commit && config.auto_commit`; `should_push = opts.push || (config.auto_push && should_commit && !opts.isolated)`.
- Commit message: `act-op: <id_short> <op_type>`. `<id_short>` uses the shortest-unique-prefix per act-6991 at the time of commit; for newly-created ids the full 16 hex is used because no fold has materialized the prefix yet.
- Failure paths:
  - `git add` non-zero → return without staging; surface error.
  - `git commit` non-zero → run `git restore --staged <op_path>`; surface error; do not delete the op file (the user may retry).
  - `git push` non-zero → commit stays; surface push error with exit code from git; structured error class is `op_invalid` only if push rejection was due to non-fast-forward (caller may retry after rebase).

## Out of scope
- Atomic claim's pull-rebase (act-9824) — that flow handles its own pull-rebase between commit and push.
- Hook execution mechanics (act-ce9f) — this issue calls into hooks but does not define them.
- Op file write itself (act-6ec9 for naming/shard probe) — this issue assumes the op file is already on disk.

## Implementation notes
- The commit must touch only `.act/ops/**` (and possibly `.act/imports/**` for bootstrap and `.act/config.json` for last_hlc updates that are sequenced separately). Reject if `git diff --cached --name-only` after `git add` reports paths outside that allow-list.
- Push remote/branch: discover via `git rev-parse --abbrev-ref --symbolic-full-name @{u}`; if no upstream, `--push` exits 2 with usage error.
- Avoid invoking `git` in a shell; use exec-style calls so paths with spaces are safe.
- Single concurrency lock: hold `.act/.lock` (POSIX flock / Windows LockFileEx) around the whole commit_op sequence so two writers cannot interleave staging.

## Test plan
- Unit: flag precedence — every cell of the §4 precedence rules.
- Unit: commit_op with --no-commit returns committed=false, no git invocation beyond add.
- Unit: commit message format pinned by golden.
- Integration: write one op, run commit_op, assert HEAD has exactly one commit touching the op path.
- Integration: --push with no upstream → exit 2.
- Integration: commit failure (simulate via injected git wrapper that rejects) → op file remains, staged area cleaned.
- Integration: post-create hook fails → no commit, op file removed by the hook layer (act-ce9f); commit_op returns hook_failed.
