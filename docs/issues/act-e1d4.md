---
title: "act ready command"
deps: [act-912f, act-6991, act-296e]
acceptance_criteria:
  - "An issue is `ready` iff `status == open` AND no incoming `blocks` dep points at it from an open or in_progress issue."
  - "Sort order: priority asc, created_at desc, id asc as tie-breaker."
  - "`--under <id>` restricts output to descendants (via `parent` edges, transitive) of the given id; the id is resolved through the pipeline."
  - "`--limit N` defaults to 50 and bounds the result count."
  - "JSON output: `{\"ok\": true, \"count\": N, \"issues\": [{id, prefix, title, priority, type}, ...]}`."
status: closed
created_at: 2026-04-29T00:00:00Z
closed_at: 2026-04-29T00:00:00Z
---

# act ready command

## Context
Spec §3 `act ready` (lines 717–733). Computes the ready frontier of the issue graph: issues that are `open` and not blocked by any non-terminal blocking issue. Reads from the folded index (act-912f), uses shortest-unique-prefix display (act-6991).

## Scope
- Parse flags `--under`, `--json`, `--limit`.
- Fold all issues (or read from index post-validation).
- Compute ready set: `status == open` AND no incoming `blocks` edge from an open/in_progress source.
- If `--under` is provided, resolve and restrict to its parent-tree descendants (transitive `parent` edges).
- Sort and limit; emit JSON or human form.

## Out of scope
- Mutation of any kind.
- `blocks` edges from closed/blocked sources do NOT block readiness (only `open`/`in_progress` sources count).
- Cross-tree dependency analysis beyond the basic blocks/parent check.

## Implementation notes
- Flags:
  - `--under <id>` string, optional. Resolved via id pipeline; exit 3 if not found, exit 2 if ambiguous.
  - `--json` bool.
  - `--limit N` int, default 50.
- Exit codes: `0`; `2` bad flags or ambiguous `--under`; `3` `.act/` missing or `--under` id not found; `4` writer-version skew during fold/rebuild.
- Algorithm:
  1. Validate fold-checkpoint; rebuild index if stale.
  2. Load all issues with their `blocks` reverse-edges and `parent` chains.
  3. Build ready set in one pass: `{i for i in issues if i.status == "open" and no e in edges where e.target == i and e.type == "blocks" and e.source.status in ("open", "in_progress")}`.
  4. If `--under`, traverse parent edges from `--under`'s descendants and intersect.
  5. Sort by (priority asc, created_at desc, id asc).
  6. Apply `--limit`.
- JSON schema: `{"ok": true, "count": <int>, "issues": [{"id": "act-<16hex>", "prefix": "act-<short>", "title": "...", "priority": <int>, "type": "..."}]}`.
- Side effects: may rebuild the index; never writes ops.

## Test plan
- Empty repo: `count: 0`, exit 0.
- Single open issue with no deps: returned as ready.
- Open issue blocked by another open issue: NOT in ready set.
- Open issue blocked only by a closed issue: IS in ready set.
- Open issue blocked by an `in_progress` issue: NOT ready.
- `relates`/`supersedes` edges do not affect readiness.
- `--under <epic-id>`: returns only issues whose parent chain leads to `<epic-id>`.
- `--under <unknown>`: exit 3.
- `--under <ambiguous>`: exit 2.
- `--limit 1`: returns one issue (top per sort).
- Stable sort: ties broken by created_at desc then id asc.
- Stale index: rebuilt before computation; results match fresh fold.
- Repo containing op with `writer_version > self`: exit 4.
