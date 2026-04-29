---
title: "Hooks runtime contract"
deps: [act-c9f0, act-5ca9]
acceptance_criteria:
  - "Only three hook filenames are loaded: .act/hooks/post-create, .act/hooks/post-close, .act/hooks/post-claim; any other filename is ignored"
  - "Reserved future names (pre-commit-op, post-fold, post-compact) are explicitly refused: act loads them as no-ops with no warning so future binaries can adopt the names"
  - "Hook resolution: by op-type. create -> post-create, close -> post-close, claim -> post-claim. Other op-types skip hook execution entirely."
  - "If the hook file is absent or not executable, hook is skipped silently (no error)"
  - "Hook is spawned synchronously, cwd = repo root, stdin = the op JSON exactly one line with no trailing newline, EOF after"
  - "Env vars set: ACT_OP_ID, ACT_OP_TYPE, ACT_ISSUE_ID, ACT_HOOK_PHASE=pre-commit-op; no other ACT_* vars"
  - "Stdout and stderr captured up to 64KB each; overflow truncates and tags details.truncated=true; on success they are discarded"
  - "Wall-clock timeout 5s; on timeout SIGTERM, 1s grace, then SIGKILL; treated as failure"
  - "On non-zero exit or timeout: run `git restore --staged <op-path>`, delete the op file, emit hook_failed error with stderr_tail = last 4096 bytes UTF-8-trimmed (per §5.D.3); exit 7"
  - "Hooks NEVER run on: act fold (read-only), replay/recovery, act import, fresh git clone — invariant: exactly once per logical op on the originating writer"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Hooks runtime contract

## Context
Spec §"Errors, hooks, migration..." §2 ("Hooks contract") specifies the discovery, execution, and failure semantics. Hooks fire between op-write/stage and op-commit so a non-zero hook exit cleanly aborts the op. Spec §5.D.3 pins the stderr_tail truncation rule.

## Scope
- Implement `run_hook(op_type, op_envelope, op_path) -> HookResult`:
  - Resolve filename: `post-create`, `post-close`, or `post-claim`.
  - Stat the file under `.act/hooks/`. Absent → return `{ran: false, ok: true}`. Not executable → return same.
  - Reserved names (`pre-commit-op`, `post-fold`, `post-compact`) MUST NOT be loaded even if present and executable. Stat them and skip silently.
- Spawn:
  - Working directory = repo root (the directory containing `.act/`).
  - Stdin pipe gets `canonical_json(op_envelope)` exactly, no trailing newline; close stdin after write.
  - Env: copy parent env, add `ACT_OP_ID=<op.op_hash>`, `ACT_OP_TYPE=<op_type>`, `ACT_ISSUE_ID=<op.issue_id>`, `ACT_HOOK_PHASE=pre-commit-op`. No other ACT_* additions; future env vars are additive — document this guarantee.
  - Capture stdout and stderr separately, each capped at 64KB. Beyond cap, drop bytes and set `truncated=true`.
- Timeout: 5s total wall time. After 5s send SIGTERM; wait 1s; if still alive, SIGKILL. Either signal path is `hook_failed`.
- On success (exit 0): drop captured output, return `{ran: true, ok: true}`.
- On failure (non-zero exit or timeout):
  - Compute `stderr_tail = last 4096 bytes of captured stderr, then UTF-8-trim invalid trailing bytes` (per §5.D.3).
  - Run `git restore --staged <op_path>` to unstage.
  - Delete the op file from disk.
  - Return `{ran: true, ok: false, error: HookFailed{hook, exit_code, stderr_tail, truncated}}`.
- Caller (act-5ca9 commit_op) maps `HookFailed` to exit 7 with the §1 error envelope.

## Out of scope
- act import skipping hooks (act-6eff handles its own no-hook path; this issue just guarantees it never invokes run_hook).
- Compaction's post-compact reservation (act-a0ad) — reserved name only; this issue refuses to run it.

## Implementation notes
- The "exactly once per logical op on the originating writer" invariant means hooks must NOT run during fold replay (e.g., a fresh `git clone` that reads existing ops). Since fold is the only path that processes pre-existing ops without writing, and fold never calls run_hook, this is enforced by construction.
- UTF-8 trim: scan back from byte 4096 to find the last byte that is the start of a complete UTF-8 sequence; truncate there. Avoids splitting multi-byte chars in the middle.
- On Windows, signals: use `Process.Kill` (SIGKILL equivalent) and rely on the OS' graceful-stop API for the 1s grace window. Document the Windows path in code comments.
- Race: hook deletion of op file must happen *after* stage-revert, not before, to keep the working tree consistent if revert fails.

## Test plan
- Unit: hook file absent → skipped, op stays.
- Unit: hook not executable (chmod -x) → skipped, op stays.
- Unit: hook exits 0 → committed, no stderr leakage.
- Unit: hook exits 1 with stderr "boom" → op file deleted, staged area clean, hook_failed error with stderr_tail="boom".
- Unit: hook prints 100KB to stderr → stderr_tail is last 4096 bytes UTF-8-trimmed, truncated=true.
- Unit: hook sleeps 10s → SIGTERM after 5s, SIGKILL after 6s, hook_failed.
- Unit: env vars set correctly — hook script echoes them, parent asserts.
- Unit: reserved name `pre-commit-op` ignored even with valid post-create absent.
- Integration: stdin payload is canonical JSON of the op envelope, single line, no trailing newline.
