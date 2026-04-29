---
title: "SQLite index schema and rebuild"
deps: [act-296e, act-a1f6]
acceptance_criteria:
  - "index.db tables match §2.6 exactly: issues, deps, accept, fts (fts5), meta — with the listed column types and primary keys"
  - "Indices created: idx_issues_status_priority, idx_issues_parent, idx_deps_parent, idx_deps_child"
  - "meta.tree_hash mirrors the current .act/ops git tree SHA after every successful rebuild"
  - "On every command load: if meta.tree_hash != git_tree_hash('.act/ops'), the index is rebuilt before serving any read query"
  - "Rebuild is incremental when the fold checkpoint reports a partial hit: only refolded issues' rows are deleted+reinserted; full-hit short-circuits and does no DB writes"
  - "index.db is gitignored; act-init writes the .gitignore entry per §2.5"
  - "fts5 content table is populated with id (UNINDEXED), title, description for full-text search; act search (act-0a22) consumes this table"
  - "Index corruption (sqlite OPEN error or schema_version mismatch on meta) emits index_corrupt error code (exit 9) suggesting `act doctor --rebuild`"
status: open
created_at: 2026-04-29T00:00:00Z
---

# SQLite index schema and rebuild

## Context
Spec §2.6 ("`.act/index.db` schema") defines a derived SQLite index that powers `act list`, `act show`, `act ready`, `act search`, and `act log` reads. The index is *never* the source of truth — it is rebuilt from the fold output. Spec §3.5 ties rebuild staleness to the fold checkpoint's tree_hash.

## Scope
- Implement `open_or_init_index(path) -> *DB`:
  - Open `.act/index.db` with `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`.
  - If empty, create the schema:
    - `issues(id PK, title, description, status, priority, type, parent, assignee, created_at, closed_at, closed_reason, closed_by_tree)`
    - `deps(child, parent, edge, PRIMARY KEY(child, parent))`
    - `accept(issue, idx, text, done, PRIMARY KEY(issue, idx))`
    - `fts USING fts5(id UNINDEXED, title, description, content='')`
    - `meta(key PRIMARY KEY, value)` seeded with `schema_version=1` and `tree_hash=''`.
  - Create indices: `idx_issues_status_priority`, `idx_issues_parent`, `idx_deps_parent`, `idx_deps_child`.
- Implement `rebuild(db, fold_result) -> tree_hash`:
  - Wrap in a single transaction.
  - For full rebuild: `DELETE FROM issues; DELETE FROM deps; DELETE FROM accept; INSERT ... SELECT` from fold output; `INSERT INTO fts(id, title, description) ...`.
  - For partial rebuild (act-a1f6 supplies a list of refolded issue ids): DELETE rows for those ids in issues/deps/accept/fts and re-insert.
  - Update `meta.tree_hash` to current `.act/ops` git tree hash.
- Implement `ensure_fresh(db) -> bool`:
  - Compare `meta.tree_hash` to `git_tree_hash('.act/ops')`.
  - On mismatch: invoke fold via act-9362 + act-a1f6, then rebuild.
  - On match: no-op.
  - Return whether a rebuild happened (so callers can log it under `--verbose`).
- `closed_by_tree` column: populated by doctor (act-40ae) and compaction (act-a0ad); leave NULL on initial rebuild.
- Emit `index_corrupt` (exit 9) when:
  - sqlite returns SQLITE_CORRUPT or SQLITE_NOTADB on open
  - `meta.schema_version` exists and != 1 (forward-incompatible index)
  - schema introspection finds a table missing or with mismatched columns

## Out of scope
- act search FTS query syntax (act-0a22).
- Doctor's index repairs (act-40ae).
- Compaction's writes to `closed_by_tree` (act-a0ad).
- act init's gitignore wiring beyond verifying it (act-b0b9 owns init).

## Implementation notes
- Always rebuild from the fold result; never accept partial writes from a write command directly. Writes go to op files; the index updates next read.
- Use prepared statements for the rebuild loop; rebuilding 1000 issues should fit in a single transaction in well under a second.
- fts5 `content=''` ("contentless table") means we manage rows ourselves via INSERT with the same rowid; on partial rebuild use `DELETE FROM fts WHERE id = ?` then `INSERT`.
- Transaction failure rolls back; do not leave the DB in a half-rebuilt state. Callers should retry once on SQLITE_BUSY.
- The bytes of `index.db` are explicitly *not* reproducible (page allocation, free list); only query results are. This is the §3.6 determinism contract.

## Test plan
- Unit: open_or_init_index on empty path creates all tables and indices; PRAGMA queries verify schema.
- Unit: rebuild from a fold of 10 issues populates 10 issues rows, deps rows for each (child,parent) pair, accept rows for each criterion, fts entries.
- Unit: ensure_fresh returns false when meta.tree_hash matches; true after a write that changes the tree.
- Unit: corrupted DB file (truncated header) → index_corrupt exit 9.
- Integration: partial rebuild — 1 of 5 issues changed → only that issue's rows are deleted/reinserted (assert via injected DELETE counter).
- Determinism: query results from rebuilt DB equal fold-output JSON for every issue (feeds §3.6 test 1 indirectly).
