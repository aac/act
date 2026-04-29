---
title: "act show command"
deps: [act-912f, act-6991]
acceptance_criteria:
  - "`act show <id>` resolves the input through the id-resolution pipeline (import maps → exact full id → prefix match), folds the issue, and prints the snapshot."
  - "Ambiguous prefix exits 2 with `{\"error\":{\"kind\":\"ambiguous_id\",\"candidates\":[...]}}`; zero matches exits 3 (`not_found`) per §5.C.1."
  - "`--include-ops` inlines the full op stream alongside the folded snapshot under an `ops` key."
  - "JSON output contains: id, title, description, status, priority, type, parent, deps[{id,type}], assignee, acceptance_criteria, created_at, closed_at, closed_reason, ops_count."
  - "Writer-version skew during fold exits 4."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# act show command

## Context
Spec §3 `act show <id>` (lines 613–635). Reads ops for one issue, folds them through the §2 op-fold algorithm, and emits the materialized snapshot. Pre-import id resolution per §3 lines 523–531.

## Scope
- Parse positional `<id>` and flags `--json`, `--include-ops`.
- Resolve id via the pipeline (imports, full id, prefix).
- Fold the issue's op stream (using checkpoint snapshot if available).
- Emit JSON snapshot; optionally include the raw op list.

## Out of scope
- Mutating any op (read-only).
- Listing or filtering across issues.
- Op stream reconstruction beyond the resolved id.

## Implementation notes
- Flags:
  - `--json` bool.
  - `--include-ops` bool, default false. When true, emits the full HLC-sorted op array under `ops`.
- Exit codes: `0`; `2` ambiguous prefix or unknown flag; `3` id not found OR `.act/` missing (§5.C.1 unifies env errors and resolution misses under exit 3); `4` writer-version skew during fold.
- JSON schema:
  ```
  {"ok": true,
   "issue": {"id":"act-<16hex>","title":"...","description":"...",
             "status":"...","priority":<int>,"type":"...","parent":"..."|null,
             "deps":[{"id":"...","type":"blocks|relates|supersedes"}],
             "assignee":"..."|null,"acceptance_criteria":["..."],
             "created_at":"...","closed_at":"..."|null,"closed_reason":"..."|null,
             "ops_count": <int>},
   "ops": [/* present iff --include-ops */]}
  ```
- Ambiguous resolution surfaces the candidate list (sorted lexicographically) under `error.candidates`.
- Side effects: none. May read snapshots and op files.

## Test plan
- Show with full id: returns snapshot, `ops_count` matches files on disk.
- Show with shortest unique prefix: same result.
- Show with ambiguous prefix: exit 2, JSON contains `kind: ambiguous_id` and candidates.
- Show with prefix matching zero ids: exit 3 (`not_found`).
- Show outside `.act/`: exit 3.
- `--include-ops` includes HLC-ordered op array; absent without the flag.
- Closed issue: `closed_at` and `closed_reason` populated.
- Issue with deps: `deps` array reflects all surviving (non-removed) edges with correct types.
- Import-map remapped id: input matched via `.act/imports/*.json` resolves to the post-import id.
- Op stream containing a `writer_version > self`: exit 4.
- Verify JSON keys appear in stable order; `prefix` consistent with `act-6991` shortest-unique-prefix rules when emitted in surrounding contexts (note: `show` returns the full id only).
