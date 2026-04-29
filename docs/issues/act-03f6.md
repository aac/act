---
title: "act dep add command"
deps: [act-3bbe, act-5ca9, act-912f]
acceptance_criteria:
  - "`act dep add <child> <parent>` resolves both ids via the pipeline, refuses cycles in the `blocks` subgraph (cycle detection over the full folded graph), and writes a `dep-add` op."
  - "Cycle detection applies only to `--type blocks`; `relates` and `supersedes` are NOT cycle-checked."
  - "Cycle refusal exits 1 with `{\"error\":{\"kind\":\"cycle\",\"path\":[...]}}`; self-edge exits 2."
  - "Duplicate-edge dedup key is `(child, parent, type)` per §5.C.5; exact match is idempotent (no op, exit 0); same pair with different `--type` produces a new op."
  - "JSON output: `{\"ok\": true, \"child\": \"...\", \"parent\": \"...\", \"type\": \"blocks|relates|supersedes\", \"committed\": <bool>}`."
status: open
created_at: 2026-04-29T00:00:00Z
---

# act dep add command

## Context
Spec §3 `act dep add <child> <parent>` (lines 695–713). Adds a dependency edge to the issue graph. Cycle and dedup semantics per §5.C.5.

## Scope
- Parse positional `<child>` and `<parent>`, flags `--type`, `--json`, plus universal write flags.
- Resolve both ids via the pipeline.
- Reject self-edges (exit 2).
- For `--type blocks`: run cycle detection over the full folded graph (all blocks edges).
- Dedup: if an edge with the exact `(child, parent, type)` triple already exists, no-op exit 0.
- Otherwise emit a `dep-add` op and op-commit.

## Out of scope
- Removing edges (handled by `act update --dep-rm`).
- Bulk edge addition.
- Validating that the edge target is in any particular status (closed parents are allowed).

## Implementation notes
- Flags:
  - `--type T` enum `blocks|relates|supersedes`, default `blocks`.
  - `--json` bool.
  - Universal write flags apply.
- Exit codes:
  - `0` success or idempotent dedup.
  - `1` cycle detected (only for `blocks`).
  - `2` bad flags, self-edge, ambiguous prefix.
  - `3` `.act/` missing or either id not found.
  - `4` writer-version skew during fold.
- JSON output (success): `{"ok": true, "child": "act-<16hex>", "parent": "act-<16hex>", "type": "...", "committed": <bool>}`.
- JSON output (cycle): `{"error": {"kind": "cycle", "path": ["act-<id>", "act-<id>", ...]}}` with `path` listing the cycle through `blocks` edges including the proposed new edge.
- Side effects: 0 or 1 op file written; 0 or 1 git commit.
- Cycle algorithm: build directed graph of all live `blocks` edges from the folded index, add the proposed edge, run a DFS from `child` checking if `child` is reachable from itself.

## Test plan
- Add `blocks` edge between two unrelated issues: op written, exit 0.
- Add the same edge again: exit 0, no new op (dedup per §5.C.5).
- Add same `(child, parent)` with `--type relates`: new op (different type), exit 0.
- Self-edge `act dep add A A`: exit 2.
- Add an edge that would create a cycle in `blocks`: exit 1, JSON contains cycle path.
- Add a `relates` edge that would form a cycle in the `relates` graph: exit 0 (relates is not cycle-checked).
- Add a `supersedes` edge that would form a cycle: exit 0.
- Ambiguous prefix on either arg: exit 2.
- Unknown id on either arg: exit 3.
- `--no-commit`: op staged, `committed: false`.
- `--no-commit --push`: exit 2.
- Add edge to a closed parent: allowed, exit 0.
- Verify cycle path field starts and ends with the same id and traverses only `blocks` edges.
