---
title: "MCP composed tools act_next act_finish act_block"
deps: [act-380d, act-9824, act-40ae]
acceptance_criteria:
  - "act_next input: {under?: string, read_only?: bool}; behavior: ready → claim first candidate → show"
  - "act_next on claim loss retries with exponential backoff: 3 attempts at 100ms, 400ms, 1.6s with ±25% jitter (uniform [0.75x,1.25x], re-rolled per attempt per §5.D.1)"
  - "act_next refolds and excludes just-lost ids each attempt"
  - "act_next on exhaustion returns {claimed:false, candidates:[...]} without claiming"
  - "act_next on success returns {claimed:true, issue:{...}}"
  - "act_finish input: {id:string, reason?:string}; writes close op with commit message containing '(act-XXXX)' so doctor orphan-close can grep"
  - "act_finish output equals act close output"
  - "act_block input: {id:string, blocked_by:string, reason?:string}; writes set-status=blocked then dep-add type=blocks atomically (staged writes, single-commit per §5.D.2)"
  - "act_block output: {ok:true, id, blocked_by, ops_written:['set-status','dep-add']}"
  - "All three composed tools mark themselves recommended in tool descriptions; the 1:1 surface is described as escape hatches"
status: closed
created_at: 2026-04-29T00:00:00Z
---

# MCP composed tools act_next act_finish act_block

## Context
Implements the three composed MCP tools per spec-v2 §"MCP tool surface" and §5.D clarifications. These are the recommended path for agents driving `act`; they wrap the 1:1 verb surface with claim-loop and atomicity semantics that are tedious to reproduce client-side.

## Scope
- Register three tools alongside the 1:1 surface from act-380d:
  - `act_next` — `act ready` + claim-first-candidate + `act show`, with bounded-retry on claim loss.
  - `act_finish` — `act close --reason ...` with a commit message that embeds `(act-<prefix>)` so `act doctor orphan-close` can correlate.
  - `act_block` — atomic `set-status=blocked` + `dep-add type=blocks` via staged writes, single-commit semantics.
- Backoff math for `act_next`: 3 attempts at base delays 100ms, 400ms, 1.6s; jitter is uniform `[0.75x, 1.25x]` of the base, re-rolled per attempt (§5.D.1).
- Refold between attempts so newly-arrived ops are visible; exclude ids that lost their claim this run.
- Atomicity for `act_block`: stage the two op files in a worktree-local index, commit once, push once. Failure between stages aborts cleanly with no partial state.
- Tool descriptions: each composed tool's description marks it as the recommended path; descriptions of `act_ready`, `act_update --claim`, `act_close`, `act_dep_add` are amended to read as escape hatches.

## Out of scope
- Server scaffold, 1:1 surface (act-380d).
- Doctor's `orphan-close` check itself (act-40ae); this issue only ensures the marker is written.
- Generalized multi-op transactions: only `act_block` needs them in v1.

## Implementation notes
- The claim loop uses an injectable clock so tests can assert exactly 3 attempts deterministically (§5.D.5).
- `act_finish` commit-message marker uses the shortest-unique prefix at write time; the marker remains stable thereafter even if a future prefix collision extends the display prefix elsewhere.
- `act_block` staged writes: write both op files to a temp dir, then atomic-rename into `.act/ops/<id>/<yyyy-mm>/`, then `git add` both, then one `git commit`. Hooks fire once.
- All three composed tools accept `read_only:bool`; when set, they refuse with the same `read_only_violation` envelope as the server-level flag (which still overrides).

## Test plan
- Spec §7.5 (MCP end-to-end):
  - Tool list contains the three composed tools plus the 1:1 surface.
  - `act_next` returns `{claimed:true, issue:{...}}` on a fresh ready queue.
  - `act_next` on a contended queue returns `{claimed:false, candidates:[...]}` after the bounded-retry budget; with injected clock, exactly 3 claim attempts (§5.D.5).
  - `act_finish` writes a close op and returns `{closed:true, id:...}`; commit message contains `(act-<prefix>)`.
  - `act_block` writes status=blocked and a dep edge atomically; assert the single commit contains both op files.
- Jitter test: stub the RNG, assert per-attempt jitter is re-rolled and stays in `[0.75x, 1.25x]`.
