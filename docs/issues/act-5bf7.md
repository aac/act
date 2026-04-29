---
title: "act list command"
deps: [act-912f, act-6991]
acceptance_criteria:
  - "`act list` reads `.act/index.db` after a fold-checkpoint validation; if the tree-hash mismatches, the index is rebuilt automatically before the query runs."
  - "`--status` accepts a CSV of statuses; default is all non-closed. `--assignee` is exact-match; `--type` filters by enum; `--limit` defaults to 200."
  - "`--sort` accepts `priority|created_at|closed_at|id` with optional `:asc`/`:desc`; default sort is priority asc, created_at desc, then id asc as tie-breaker."
  - "JSON output: `{\"ok\": true, \"count\": N, \"issues\": [{id, prefix, title, status, priority, type, assignee, parent, created_at}, ...]}`."
  - "Empty result returns `count: 0` and exit 0; `--limit 0` or unknown sort field exits 2."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# act list command

## Context
Spec §3 `act list` (lines 584–609). Read-only query against the SQLite index (§1.6.2 schema, act-912f). Uses shortest-unique-prefix display (§1.8, act-6991) per row.

## Scope
- Parse `--status`, `--assignee`, `--type`, `--json`, `--limit`, `--sort` flags.
- Validate fold-checkpoint tree-hash; rebuild `.act/index.db` if stale.
- Run filtered, sorted, limited query against the index.
- Compute shortest-unique-prefix per row for the `prefix` field.
- Emit JSON or stable human-readable output.

## Out of scope
- Mutating any op or commit.
- Full-text search (handled by `act search`).
- Folding individual issues (already fully materialized in the index).

## Implementation notes
- Flags:
  - `--status X` CSV of `open|in_progress|blocked|closed`. Default: all non-closed.
  - `--assignee Y` string, exact match. Default: any.
  - `--type T` enum `task|bug|epic|chore`. Default: any.
  - `--json` bool.
  - `--limit N` int, default 200. `--limit 0` exit 2.
  - `--sort field[:asc|:desc]` enum `priority|created_at|closed_at|id`. Default: priority asc, created_at desc; id asc tie-breaker. Unknown field exit 2.
- Exit codes: `0`; `2` bad flag (unknown sort field, unknown enum, `--limit 0`, malformed CSV); `3` `.act/` missing; `4` writer-version skew encountered during automatic rebuild.
- JSON schema: `{"ok": true, "count": <int>, "issues": [{"id": "act-<16hex>", "prefix": "act-<short>", "title": "...", "status": "...", "priority": <int>, "type": "...", "assignee": "..."|null, "parent": "..."|null, "created_at": "<rfc3339>"}]}`.
- Side effects: may trigger an index rebuild (file I/O on `.act/index.db`); never writes ops.
- Empty result is success: `{"ok": true, "count": 0, "issues": []}` with exit 0.

## Test plan
- Default invocation in a populated repo: returns all non-closed issues, sorted per default rules.
- `--status open,in_progress`: filters to those statuses only.
- `--assignee agent-1`: exact match; `agent-1x` is excluded.
- `--type bug`: only bug issues.
- `--limit 5`: at most 5 issues.
- `--limit 0`: exit 2.
- `--sort created_at:desc`: most-recent first; tie-broken by id asc.
- `--sort foo`: exit 2.
- `--status` with malformed enum (`opn`): exit 2.
- Stale tree-hash: index rebuilt before query, results consistent with fresh fold.
- Empty repo (no issues): `count: 0`, exit 0.
- Force a `writer_version > self` op into the tree, run with stale index: exit 4 during rebuild.
- Verify `prefix` per row is the shortest unique prefix at query time.
