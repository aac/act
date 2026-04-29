---
title: "act close command"
deps: [act-3bbe, act-5ca9, act-912f, act-ce9f, act-296e]
acceptance_criteria:
  - "`act close <id>` writes a `close` op carrying `closed_at` and `closed_reason`, computes and stores the `closed_by_tree` reverse index (git tree hash of `.act/ops/<id>/`), and op-commits with message `act-op: <id> close`."
  - "`post-close` hook runs after the op file is written and before the op-commit; non-zero hook exit returns command exit 1."
  - "Idempotent: closing an already-closed issue re-emits no op and exits 0; only a true conflict (per §2.3 LWW resolution for status) returns 1."
  - "JSON output: `{\"ok\": true, \"id\": \"...\", \"closed_at\": \"<rfc3339>\", \"closed_reason\": \"...\", \"committed\": <bool>, \"pushed\": <bool>}`."
  - "`--reason` >4KB exits 2; closing an issue with open children is allowed (surfaced separately by `doctor orphan-close`)."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# act close command

## Context
Spec §3 `act close <id>` (lines 673–691). Emits a terminal status op (status field uses accept-once semantics per §2.3 / act-296e). Closed-by-tree reverse index is computed at close time for orphan-close detection by doctor.

## Scope
- Parse positional `<id>` and flags `--reason`, `--json`, plus universal write flags.
- Resolve id via pipeline.
- Compute `closed_at` from local HLC; assemble close op payload `{closed_at, closed_reason, closed_by_tree}`.
- Compute `closed_by_tree = git tree hash of .act/ops/<id>/` immediately before write.
- Write op file; run `post-close` hook; op-commit with message `act-op: <id> close`.
- Detect already-closed (idempotent no-op exit 0) vs true status conflict (exit 1).

## Out of scope
- Reopening (separate `reopen` op; out-of-scope command).
- Cascading close to children.
- Close validation against open dependencies (deferred to doctor).

## Implementation notes
- Flags:
  - `--reason TEXT` string, default `""`. Stored as `closed_reason`. Reason >4KB exits 2.
  - `--json` bool.
  - Universal write flags: `--no-commit`, `--push`, `--isolated`, `--verify`.
- Exit codes:
  - `0` success or idempotent no-op.
  - `1` true conflict per §2.3 status resolution OR hook reject.
  - `2` bad flags (reason >4KB, illegal flag combos), ambiguous prefix.
  - `3` `.act/` missing or id not found.
  - `4` writer-version skew during fold.
- JSON output: `{"ok": true, "id": "act-<16hex>", "closed_at": "<rfc3339>", "closed_reason": "...", "committed": <bool>, "pushed": <bool>}`.
- Idempotency: if folded snapshot already shows `status == closed`, do nothing — no op file, no commit, exit 0 with the existing `closed_at`/`closed_reason` echoed.
- Side effects: one op file, optional hook, op-commit, optional push.
- `closed_by_tree`: must be the git tree hash of `.act/ops/<id>/` AT WRITE TIME (not after the new op file is added). This is the input the doctor's `orphan-close` check correlates against.

## Test plan
- Close an open issue: op written, commit message `act-op: <id> close`, JSON ok, exit 0.
- Close already-closed issue: no op file added, exit 0, idempotent.
- True status conflict (concurrent reopen race wins under §2.3): exit 1.
- `--reason "shipped"`: stored verbatim.
- `--reason` of 4097 bytes: exit 2.
- `post-close` hook returning 1: exit 1.
- Closing issue with open children: exit 0 (doctor surfaces it separately).
- `--no-commit`: op staged, no commit, `committed: false`.
- `--no-commit --push`: exit 2.
- `--isolated --push`: exit 2.
- Unknown id: exit 3.
- Ambiguous prefix: exit 2.
- Verify `closed_by_tree` equals `git ls-tree HEAD .act/ops/<id>/`-equivalent hash captured pre-write.
