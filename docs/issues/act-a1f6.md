---
title: "Fold checkpoint"
deps: [act-296e]
acceptance_criteria:
  - "Checkpoint file is `.act/fold-checkpoint.json` with shape {schema_version: 1, tree_hash: <git-tree-sha-of-.act/ops>, issues: {<id>: {subtree_hash, fold_hash}}} per §3.5"
  - "load_or_fold() returns the cached fold when cp.tree_hash == git_tree_hash('.act/ops'); otherwise refolds only issues whose subtree_hash changed and writes a refreshed checkpoint"
  - "git_tree_hash(path) returns the literal git tree SHA-1; for unstaged changes, computed via `git ls-files -s` + `git mktree`; matches a committed tree byte-identically"
  - "fold_hash is sha256 of canonical_json(issue_state) computed via the act-b545 serializer"
  - "Missing subtrees in the current tree drop the corresponding issue from the new checkpoint (per §5.B.5)"
  - "Checkpoint reuse on a stale checkpoint produces fold output byte-identical to a cold-cache fold (the determinism contract test §3.6.3)"
  - "Crash mid-checkpoint-write leaves either the old checkpoint intact or a temp file ignored by readers; atomic via write-temp + fsync + rename"
status: closed
created_at: 2026-04-29T00:00:00Z
---

# Fold checkpoint

## Context
Spec §3.5 specifies a derived cache that lets fold skip subtrees whose git tree SHA matches the checkpoint. Subtree hashes are invariant under squash/rebase, so the cache survives history rewrites cleanly. This issue implements the cache; act-9362 is the cold-fold path it falls back to.

## Scope
- Implement `read_checkpoint() -> Checkpoint?` reading `.act/fold-checkpoint.json`; missing or schema_version mismatch returns nil (treated as cold cache).
- Implement `git_tree_hash(path) -> string`:
  - For tracked + clean: shell to `git rev-parse HEAD:<path>` (or fall back to `git ls-tree HEAD <path>` parsing).
  - For unstaged changes: `git ls-files -s -- <path>` to build entries, pipe to `git mktree` to compute the SHA without staging.
- Implement `load_or_fold(ops_root) -> (issues, index)`:
  - Compute current `.act/ops` tree hash.
  - Full hit (cp.tree_hash == current): return cached fold; cached `fold_hash` per id is enough to rebuild the in-memory `issues` map by reading per-issue snapshot side-cars (or by retaining the prior fold in process memory).
  - Partial: enumerate `cp.issues.keys() ∪ new_ids_from_diff`. New ids are detected via `git diff-tree cp.tree_hash..current -- .act/ops/` (per §5.B.5). For each id:
    - cur_sub = `git_tree_hash(".act/ops/" + id)`
    - if cur_sub == cp.issues[id].subtree_hash → keep
    - else → refold that issue from disk
  - Missing subtree (issue dir gone) → drop from new checkpoint.
- Implement `write_checkpoint(cp)` atomically: write to `.act/fold-checkpoint.json.tmp`, fsync, rename.
- Persistence of cached folded states: keep an in-process LRU and/or a side-car per-issue snapshot file under `.act/snapshots/<id>.json` that mirrors the canonical-JSON folded state. Snapshots are committed; losing them just forces a refold.

## Out of scope
- Compaction snapshots (act-a0ad) — those rewrite ops; this issue only reads them.
- SQLite index rebuild on tree-hash mismatch (act-912f) — checkpoint emits the new tree_hash; the index layer reacts to it.

## Implementation notes
- The full-hit path must not refold even one issue; that is the whole point of the cache.
- Use the same canonical-JSON serializer (act-b545) for `fold_hash` so two binaries on different platforms produce identical hashes.
- Concurrent fold runs in two processes: writers hold `.act/.lock`; checkpoint write is single-writer. Readers tolerate a stale checkpoint by treating it as a hint, never authority (per §1.2 wording for last_hlc).
- For new issues seen post-checkpoint, `git diff-tree --name-only` rooted at the prior tree hash is enough to enumerate; no need for a full glob walk.

## Test plan
- Unit: read_checkpoint on missing file returns nil; on schema_version=2 returns nil.
- Unit: git_tree_hash on a clean directory equals `git rev-parse HEAD:.act/ops`; on a directory with one unstaged op it equals the hash that `git add` + `git write-tree` would produce.
- Integration: cold fold of 50 issues writes a checkpoint; second fold with no changes returns cached result without reparsing any op file (assert via injected counter on the parser).
- Integration: modify one issue's ops; second fold refolds only that one; checkpoint updated.
- Integration: rebase that rewrites history but preserves tree contents → checkpoint still hits (subtree hashes unchanged).
- Determinism: cold fold output == warm-cache fold output, byte-identical (§3.6 test 3).
