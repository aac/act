## Data model and storage

### Fresh-eye comparison

The brief commits to a fresh-eye pass against the minimal `(id, title, body, status, deps, claim)` core. We test each candidate field on a 3-task workflow:

> **W1.** Agent A creates issue X ("rewrite parser") with two acceptance criteria.
> **W2.** Agent B claims X (assignee + status flip), discovers a sub-bug, files Y as a child of X with `blocks` edge, then closes X with a follow-up reference to Y.
> **W3.** Human runs `act ready` next morning, sees Y at the top, agent C claims and closes Y with `--reason "fixed in commit abc"`.

| Field | Earns keep? | Justification on W1-W3 |
|---|---|---|
| `id` | Yes | Required to reference X from Y, to claim, to close. |
| `title` | Yes | W3 ready-queue rendering needs a label; renames are rare but happen. |
| `description` | Yes | W1 needs space for the actual work statement; agents read this on claim. |
| `status` | Yes | Drives `ready` (W3) and claim atomicity (W2). |
| `priority` | Yes | `ready` orders by it; W3 picks Y because it inherited high priority. |
| `type` | Yes | `bug` vs `task` changes hook routing and report grouping; cheap enum. |
| `parent` | Yes | W2 makes Y a child of X; `act ready --under` scopes to a subtree. |
| `deps` (typed edges) | Yes | W2's `blocks` edge is what makes `ready` correct; `relates`/`supersedes` reuse the same primitive at zero marginal cost. |
| `assignee` | Yes | Atomic claim writes it; loss detection compares it. |
| `acceptance_criteria` | Yes | W1 sets them; W2 close requires all checked or `--reason` override. Without this field, "done" is ambiguous. |
| `created_at` | Yes | Derived from create-op HLC; required for compaction age threshold. |
| `closed_at` | Yes | Derived from close-op HLC; required for `closed_by_tree` reverse index. |
| `closed_reason` | Yes | W3 sets it; doctor uses it to distinguish abandoned vs completed. |
| labels/tags | **Dropped** | W1-W3 never need free-form tagging; `type` + `parent` cover grouping. Reintroduce only if a concrete workflow demands it. |
| estimate / time-tracking | **Dropped** | Anti-goal in brief. |
| comments | **Dropped** | Description edits + child issues cover the discussion need. |
| watchers | **Dropped** | Single-user-with-agents; no notification fan-out. |

### Issue schema (folded state)

```jsonc
{
  "id":           "act-a1b2",                  // string, full hex id, "act-" + N hex chars (N >= 4)
  "title":        "string, 1..200 chars, required",
  "description":  "string, 0..16384 chars, default \"\"",
  "status":       "open | in_progress | blocked | closed",   // default "open"
  "priority":     0,                           // int 0..3 inclusive, default 2 (lower = more urgent)
  "type":         "task | bug | epic | chore", // default "task"
  "parent":       "act-... | null",            // default null
  "deps": [
    { "id": "act-...", "edge": "blocks | relates | supersedes" }
  ],
  "assignee":     "string | null",             // default null; free-form (human handle or agent role)
  "acceptance_criteria": [
    { "text": "string, 1..500 chars", "done": false }
  ],
  "created_at":   "RFC3339 UTC, from create-op HLC wall",
  "closed_at":    "RFC3339 UTC | null",
  "closed_reason":"string | null"
}
```

Validation rules:
- `id` matches `^act-[0-9a-f]{4,40}$`.
- `priority` outside 0..3 is a hard error at write time.
- `parent` and every `deps[].id` MUST resolve to an existing (non-tombstoned) issue at fold time; doctor's `dangling-deps` check enforces this on the whole repo.
- `acceptance_criteria` indices are stable across the issue's lifetime; `remove_accept` shifts later indices down (see op semantics below).
- A close op against an issue with unmet criteria succeeds only if `closed_reason` is non-empty; doctor flags otherwise.

### ID model

Format: `act-<hex>` where the hex part is the prefix of a 40-char sha256 hex digest.

Full ID derivation at create time:

```
full_hex   = sha256(canonical_json(create_op_payload) || nonce_bytes).hex()   // 64 chars
short_hex  = full_hex[0:N]                                                    // N starts at 4
id         = "act-" + short_hex
```

- `nonce_bytes` is 16 bytes of crypto-random, embedded in the create-op payload as `"nonce": "<32 hex chars>"`. The nonce is fixed at create time and never changes; it is what guarantees two identical-titled issues created in the same wall second produce different ids.
- `N` starts at 4. Storage on disk and all references use the full 40-hex form internally; the 4-char `short_hex` is what writers stamp into the directory name and op `issue_id` field.
- Collision-extension protocol at create time:
  1. Compute `full_hex`. Set `N = 4`.
  2. Candidate id = `"act-" + full_hex[0:N]`.
  3. Acquire `.act/.lock` (advisory file lock). Scan `.act/ops/` for any directory whose name shares the candidate id's hex prefix and whose stored full id differs from `full_hex`.
  4. If a collision exists, `N += 1` and goto 2. (Practically `N` will not exceed 8 before 2^32 issues exist.)
  5. Write the create op with `issue_id = "act-" + full_hex[0:N]` and a sidecar `full_id = full_hex` field in the create payload. Release lock.
- Display: the CLI computes the shortest unique prefix across all currently-known issues per invocation (git-style). Ties are lengthened one hex at a time; the shortest unique form is what `act show`, `act list`, `act ready` print. Internally everything is keyed off the on-disk id (which already encodes the collision-extension result).
- Prefix resolution on input: a user-supplied prefix matching exactly one stored id resolves; matching zero or two-or-more is an error with a structured `{"error":"ambiguous-id","candidates":[...]}` payload.

### Op envelope

Every op file's top-level object has exactly these keys, in this order:

```jsonc
{
  "op_version":     1,
  "schema_version": 1,
  "writer_version": "0.1.0",
  "op_type":        "create",          // see enum below
  "issue_id":       "act-a1b2",        // full on-disk id, never the display prefix
  "node_id":        "7f3a91c2",        // 8 hex chars, from .act/config.json
  "hlc": { "wall": "2026-04-29T14:23:01.000Z", "logical": 0, "node_id": "7f3a91c2" },
  "payload":        { /* op-type-specific, see below */ }
}
```

`op_type` enum (all of v1):

```
create | update_field | add_dep | remove_dep | add_accept | remove_accept |
claim  | close        | redact  | import     | migrate    | tombstone
```

#### Byte-exact JSON serialization rules

Fold determinism requires that two writers given the same logical op produce byte-identical files. The canonical form is:

1. UTF-8, no BOM.
2. Top-level keys in the exact order listed above (`op_version`, `schema_version`, `writer_version`, `op_type`, `issue_id`, `node_id`, `hlc`, `payload`).
3. Nested object keys sorted lexicographically by Unicode code point.
4. Strings: minimal JSON escaping (`\"`, `\\`, `\b`, `\f`, `\n`, `\r`, `\t`, `\u00xx` for other C0/C1; non-ASCII printable code points emitted raw).
5. Numbers: integers as bare digits; no floats anywhere in the schema.
6. Whitespace: single space after `:` and `,`, no leading whitespace, no other whitespace. Two-space indent on each nested level. Newline `\n` (LF) after every `,` and after every `{` / `[` that has children. **No trailing newline at end of file.**
7. Booleans/null lowercase.
8. Empty arrays and objects emitted as `[]` / `{}` on a single line.

A reference `act fmt-op` subcommand emits this exact form; `act doctor --check op-canonical` re-serializes every op file and fails on any byte mismatch.

### Op type payloads

```jsonc
// create
{ "title": "string", "description": "string?", "priority": 2,
  "type": "task", "parent": "act-...?", "accept": ["criterion", ...]?,
  "nonce": "32 hex", "full_id": "64 hex" }

// update_field — single-field mutation; field MUST be one of the listed names
{ "field": "title|description|priority|assignee|status|type|parent",
  "value": <typed per field> }

// add_dep
{ "parent_id": "act-...", "edge_type": "blocks|relates|supersedes" }

// remove_dep
{ "parent_id": "act-..." }                  // removes any edge to that id; idempotent

// add_accept
{ "criterion": "string, 1..500 chars" }     // appended; index = current len

// remove_accept — exactly one of index/text is required
{ "index": 0 } | { "text": "exact match string" }

// claim — atomic assignee + status; sugar over two update_fields, but recorded as one op
{ "assignee": "string" }

// close
{ "reason": "string?" }                     // sets status=closed, closed_at=hlc.wall, closed_reason

// redact — replaces the named field's rendered value with "<redacted>" from this HLC forward
{ "field_path": "description | acceptance_criteria[2].text | ...",
  "new_value": "string?" }                  // optional replacement; default "<redacted>"

// import — records that this issue was materialized from an external op-log line
{ "source_ref": "issues.jsonl@<sha>", "source_id": "bootstrap-id-1",
  "mapping_file": ".act/imports/2026-04-29T14:23:01Z.json" }

// migrate — rewrites the interpretation of prior ops at fold time
{ "from_version": 1, "to_version": 2,
  "transform": { "kind": "rename_field", "from": "owner", "to": "assignee" } }

// tombstone — issue-level deletion marker
{ "deleted_at": "RFC3339 UTC" }
```

### On-disk layout

```
<repo-root>/
  .act/
    config.json                    # see schema below
    fold-checkpoint.json           # { "tree_hash": "<git-tree-of-.act/ops>", "computed_at": "...", "schema_version": 1 }
    index.db                       # SQLite, gitignored, derived
    ops/
      act-a1b2/
        2026-04/
          2026-04-29T14:23:01.000Z-7f3a9c1d-create.json
          2026-04-29T14:24:15.123Z-9c2bf014-claim.json
          2026-04-29T15:02:44.901Z-3e1f88a2-update_field.json
        2026-05/
          ...
    snapshots/
      act-a1b2.json                # canonical folded state at last compaction
    imports/
      2026-04-29T14:23:01.000Z.json   # { "source": "...", "mapping": { ... } }
    hooks/
      post-create                  # executable
      post-close                   # executable
      post-claim                   # executable
    .lock                          # advisory lock file (POSIX flock / Windows LockFileEx)
```

`.act/index.db` MUST appear in `.gitignore` (writer ensures this on `act init`). Everything else under `.act/` is committed.

#### `.act/config.json`

```jsonc
{
  "schema_version": 1,
  "node_id":        "7f3a91c2",                 // 8 hex; sha256(machine-id || git user.email)[0:8]
  "writer_version": "0.1.0",                    // version that ran `act init`
  "fold_checkpoint": ".act/fold-checkpoint.json",
  "auto_commit":    true,
  "auto_push":      false,
  "hlc_drift_budget_seconds": 300
}
```

#### `.act/index.db` schema

Tables (rebuilt from ops; never source of truth):

```
issues(
  id TEXT PRIMARY KEY, title TEXT, description TEXT, status TEXT, priority INT,
  type TEXT, parent TEXT, assignee TEXT,
  created_at TEXT, closed_at TEXT, closed_reason TEXT,
  closed_by_tree TEXT
)
deps(child TEXT, parent TEXT, edge TEXT, PRIMARY KEY(child, parent))
accept(issue TEXT, idx INT, text TEXT, done INT, PRIMARY KEY(issue, idx))
fts USING fts5(id UNINDEXED, title, description, content='')
meta(key TEXT PRIMARY KEY, value TEXT)        -- 'schema_version', 'tree_hash'
```

Indices: `idx_issues_status_priority`, `idx_issues_parent`, `idx_deps_parent`, `idx_deps_child`. `meta.tree_hash` mirrors `fold-checkpoint.json` for fast staleness check.

### Op file naming

```
<iso8601>-<hash8>-<op_type>.json
```

- `<iso8601>` is the HLC wall component, formatted as `YYYY-MM-DDTHH:MM:SS.sssZ` (millisecond precision, always UTC `Z`, fixed width 24 chars). Not local wall clock; the HLC wall is what matters for fold ordering.
- `<hash8>` is the first 8 hex chars of `sha256(canonical_envelope_bytes)` where `canonical_envelope_bytes` is the file's exact byte content per the serialization rules above. Because the hash is over the full envelope (which contains `node_id` and `hlc`), two writers cannot collide on the same payload.
- `<op_type>` is the op_type enum value, lowercase, underscores preserved.
- Collision behavior: if a file with the same `<iso8601>-<hash8>` prefix already exists in the target shard, extend the hash to 12 hex chars and retry; if 12 still collides, extend to 16; document an error past 16 (statistically impossible barring a sha256 break).

The shard directory is `ops/<issue_id>/<YYYY-MM>/` derived from the HLC wall's year-month. A writer never moves a file across shards even if the HLC wall later turns out to disagree with another machine's clock; the path is final at write time.
