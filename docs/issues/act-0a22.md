---
title: "act search command"
deps: [act-912f]
acceptance_criteria:
  - "`act search <query>` runs a SQLite FTS5 query against the index; the index is rebuilt-on-demand if stale."
  - "`--in` restricts the FTS5 column scope to `title|desc|all` (default `all`)."
  - "`--status` accepts a CSV filter; `--limit` defaults to 50."
  - "JSON output: `{\"ok\": true, \"count\": N, \"results\": [{id, prefix, title, snippet, rank}, ...]}` where `rank` is the FTS5 bm25 score (negative; closer to 0 is better)."
  - "FTS5 parse errors surface as exit 2 (usage); missing index exits 3; writer-version skew exits 4."
status: open
created_at: 2026-04-29T00:00:00Z
---

# act search command

## Context
Spec §3 `act search <query>` (lines 737–756). FTS5 query against the SQLite index built by act-912f. Read-only; rebuilds the index opportunistically if the fold-checkpoint tree-hash is stale.

## Scope
- Parse positional `<query>` and flags `--in`, `--status`, `--limit`, `--json`.
- Validate index freshness; rebuild if stale.
- Execute FTS5 query against the configured columns.
- Emit results with snippets and bm25 rank.

## Out of scope
- Substring/regex search outside FTS5 grammar.
- Mutation.
- Cross-issue summarization or aggregation.

## Implementation notes
- Flags:
  - `--in` enum `title|desc|all`, default `all`. Translates into FTS5 column constraint (`title:` / `description:` prefix or no prefix).
  - `--status X` CSV; post-FTS filter on the index `status` column.
  - `--limit N` int, default 50.
  - `--json` bool.
- Exit codes:
  - `0` success (including empty result set).
  - `2` FTS5 parse error or bad flag (unknown enum, malformed CSV, `--limit 0`).
  - `3` `.act/` or `index.db` missing.
  - `4` writer-version skew encountered during automatic rebuild.
- JSON schema: `{"ok": true, "count": <int>, "results": [{"id": "act-<16hex>", "prefix": "act-<short>", "title": "...", "snippet": "...", "rank": <float>}]}`.
- `snippet` uses FTS5 `snippet()` function with reasonable defaults (e.g., first matching column, ellipsis-bounded, ~64 char window).
- `rank` is FTS5 bm25 (lower / more negative = better); results sorted ascending by rank by default.
- FTS5 errors caught and re-emitted as `{"error": {"code": 2, "kind": "fts_parse", "message": "..."}}`.
- Side effects: may rebuild `.act/index.db`; never writes ops.

## Test plan
- Plain word query: returns matching issues with non-empty snippets.
- Phrase query (quoted): handled per FTS5 syntax.
- `--in title`: only title hits.
- `--in desc`: only description hits.
- `--in all`: hits in either column.
- `--status open,in_progress`: filters to those statuses.
- `--limit 5`: at most 5 results.
- Bad FTS5 syntax (`"unbalanced`): exit 2 with `kind: fts_parse`.
- Unknown `--in` value: exit 2.
- Empty repo / no matches: `count: 0`, exit 0.
- Missing `.act/`: exit 3.
- Stale index: rebuilt before query.
- Repo with `writer_version > self`: exit 4 during rebuild.
- Verify rank ordering ascending; verify snippets reflect actual matched terms.
