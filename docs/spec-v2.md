# act Implementation Spec

Version: 0.1.0 spec, derived from /home/user/act/docs/brief-v4.md.

## Table of contents

1. [Data model and storage](#data-model-and-storage)
2. [Op-fold and concurrency](#op-fold-and-concurrency)
3. [Commands and MCP surface](#commands-and-mcp-surface)
4. [Errors, hooks, migration, bootstrap, compaction, tests](#errors-hooks-migration-bootstrap-compaction-tests)

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
  "id":           "act-a1b2",                  // string, on-disk short id, "act-" + N hex chars (4 <= N <= 16)
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
- `id` matches `^act-[0-9a-f]{4,16}$`. IDs on disk are always the short form, capped at 16 hex chars (the collision-extension procedure in §ID model never grows past this cap). The 40-hex (actually 64-hex) sha256 digest is an internal intermediate value used to derive the short id; it is never written as an `id` or `issue_id` field.
- `priority` outside 0..3 is a hard error at write time.
- `parent` and every `deps[].id` MUST resolve to an existing (non-tombstoned) issue at fold time; doctor's `dangling-deps` check enforces this on the whole repo.
- `acceptance_criteria` indices are stable across the issue's lifetime; `remove_accept` shifts later indices down (see op semantics below).
- A close op against an issue with unmet criteria succeeds only if `closed_reason` is non-empty; doctor flags otherwise.

### ID model

Format: `act-<hex>` where the hex part is a prefix of a 64-char sha256 hex digest, **capped at 16 hex chars on disk**. IDs are always the short form; the 64-hex digest is an internal intermediate value used only to derive the short id and is never written as an `id` or `issue_id` field.

ID derivation at create time:

```
full_hex   = sha256(canonical_json(create_op_payload) || nonce_bytes).hex()   // 64 chars, internal
short_hex  = full_hex[0:N]                                                    // 4 <= N <= 16
id         = "act-" + short_hex                                               // the on-disk id
```

- `nonce_bytes` is 16 bytes of crypto-random, embedded in the create-op payload as `"nonce": "<32 hex chars>"`. The nonce is fixed at create time and never changes; it is what guarantees two identical-titled issues created in the same wall second produce different ids.
- `N` starts at 4 and may grow up to 16 if collisions force extension; see the protocol below. Everything on disk — directory names, op envelope `issue_id`, folded issue `id`, references in `parent` and `deps` — uses the short form `act-` + `full_hex[0:N]`. The 64-hex `full_hex` itself is never persisted.
- Collision-extension protocol at create time:
  1. Compute `full_hex`. Set `N = 4`.
  2. Candidate id = `"act-" + full_hex[0:N]`.
  3. Acquire `.act/.lock` (advisory file lock). Scan `.act/ops/` for any directory whose name equals the candidate id but whose stored create-op `full_hex` differs (i.e. a real id collision, not the same issue).
  4. If a collision exists, `N += 1` and goto 2. (Practically `N` will not exceed 8 before 2^32 issues exist; the hard cap is `N = 16`.)
  5. Write the create op with `issue_id = "act-" + full_hex[0:N]` and a sidecar `full_id = full_hex` field in the create payload (the only place the 64-hex digest is persisted, to support post-hoc collision verification). Release lock.
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

- `<iso8601>` is the HLC wall component, formatted as `YYYY-MM-DDTHH-MM-SS.sssZ` (millisecond precision, always UTC `Z`, fixed width 24 chars). Note the time-component separators are `-`, not `:`, because `:` is reserved in NTFS paths and breaks `git checkout` on Windows hosts before any Go code runs (act-2f3d). The shape is otherwise byte-identical to the canonical ISO-8601 form and sorts lexically the same way. The envelope JSON body still uses the canonical colon form for the HLC wall; only the filename varies. Not local wall clock; the HLC wall is what matters for fold ordering. Forward-only: parsers accept either form so pre-act-2f3d ops on disk keep folding.
- `<hash8>` is the first 8 hex chars of `sha256(canonical_envelope_bytes)` where `canonical_envelope_bytes` is the file's exact byte content per the serialization rules above. Because the hash is over the full envelope (which contains `node_id` and `hlc`), two writers cannot collide on the same payload.
- `<op_type>` is the op_type enum value, lowercase, underscores preserved.
- Collision behavior: if a file with the same `<iso8601>-<hash8>` prefix already exists in the target shard, extend the hash to 12 hex chars and retry; if 12 still collides, extend to 16; document an error past 16 (statistically impossible barring a sha256 break).

The shard directory is `ops/<issue_id>/<YYYY-MM>/` derived from the HLC wall's year-month. A writer never moves a file across shards even if the HLC wall later turns out to disagree with another machine's clock; the path is final at write time.

## Op-fold and concurrency

This section specifies the deterministic state-derivation pipeline for `act`: the
Hybrid Logical Clock, the op-fold algorithm, per-field merge semantics, the
atomic claim protocol, the fold checkpoint, and the determinism contract.

### 1. Hybrid Logical Clock (HLC)

#### 1.1 State

Each writer maintains an HLC of three fields:

```
HLC := { wall: int64_ms_unix_utc, logical: uint32, node_id: hex8 }
```

`node_id` is fixed per repo, generated at `act init` as
`sha256(machine-id || git-config user.email)[0:8]`, persisted in
`.act/config.json`. Writers never mutate `node_id` after init.

#### 1.2 Persistence

`.act/config.json` carries `last_hlc: { wall, logical }`. Every successful op
write performs an atomic update of `last_hlc` *before* the op file rename
becomes visible (write-temp + fsync + rename of `config.json` is sequenced
before staging the op file). On crash mid-write, fold reconstructs the true
high-water mark from the on-disk ops; `last_hlc` is a hint, not authority.

#### 1.3 Send (local op generation)

```
send(now_ms, prev):
  prev_wall, prev_logical = prev.wall, prev.logical
  wall_new = max(now_ms, prev_wall)
  if wall_new == prev_wall:
    logical_new = prev_logical + 1
  else:
    logical_new = 0
  assert logical_new < 2^32
  return HLC{wall_new, logical_new, node_id}
```

#### 1.4 Receive (folding an external op)

```
receive(now_ms, msg_hlc, prev):
  wall_new = max(now_ms, msg_hlc.wall, prev.wall)
  if wall_new == prev.wall and wall_new == msg_hlc.wall:
    logical_new = max(prev.logical, msg_hlc.logical) + 1
  elif wall_new == prev.wall:
    logical_new = prev.logical + 1
  elif wall_new == msg_hlc.wall:
    logical_new = msg_hlc.logical + 1
  else:
    logical_new = 0
  return HLC{wall_new, logical_new, node_id}
```

#### 1.5 Plausibility check (write-time)

```
ref = max(local_wall_now, last_seen_hlc_in_repo.wall)
if abs(op.hlc.wall - ref) > 5 * 60 * 1000:
  refuse with E_HLC_IMPLAUSIBLE
```

`last_seen_hlc_in_repo` is the maximum `hlc.wall` discovered during the most
recent fold. Fresh containers must perform one fold (or one read of
`.act/config.json:last_hlc`) before writing.

### 2. Op-fold algorithm

Input: a directory tree at `.act/ops/` (or an equivalent git tree object).
Output: an in-memory map `issue_id -> issue_state` plus a derived index.

```
fold(ops_root) -> {issues, index}:
  # (1) Discover
  files = glob(ops_root + "/*/????-??/*.json")    # monthly shards

  # (2) Parse and validate
  parsed = []
  for f in files:
    op = json.load(f)
    require(op.schema_version <= READER_SCHEMA_MAX) else E_SCHEMA_TOO_NEW
    require(op.writer_version <= READER_WRITER_MAX) else E_UPGRADE_REQUIRED
    require(known_op_type(op.op_type, op.op_version)) else E_UNKNOWN_OP
    op.op_hash = sha256(canonical_json(op.payload || op.hlc || op.node_id))
    parsed.append(op)

  # (3) Sort: HLC ascending, op_hash tiebreaker
  parsed.sort(key = (op) -> (op.hlc.wall, op.hlc.logical, op.op_hash))

  # (4) Initialize per-issue state lazily on first op for that issue_id
  issues = {}

  # (5) Apply left-to-right with per-op semantics (see 3)
  for op in parsed:
    state = issues.get(op.issue_id) or empty_issue(op.issue_id)
    issues[op.issue_id] = apply(state, op)

  # (6) Emit
  return {issues, index: build_index(issues)}
```

`empty_issue` has all scalar fields nil, sets `deps={}`, `acceptance={}`,
`tombstoned=false`, and a per-field `last_hlc` map used for LWW.

### 3. Per-field conflict resolution

The `apply(state, op)` function dispatches per `op_type`. Default merge is
last-writer-wins (LWW) by `(hlc.wall, hlc.logical, op_hash)`. The exceptions
below are normative.

| Field / op | Rule |
|---|---|
| `title`, `description`, `priority`, `type`, `assignee`, `parent` | LWW per field. Each carries its own `last_hlc`; an op with a strictly-greater HLC tuple wins; equal tuples cannot occur (op_hash differs). |
| `status` | Constrained transition table. `closed` is terminal: an op transitioning out of `closed` is ignored unless its `op_type` is `reopen` (an explicit op kind). Within `{open, in_progress, blocked}` LWW applies. `claim` ops set `(assignee, status=in_progress)` atomically (see 4). |
| `acceptance_criteria` | Grow-shrink set keyed by criterion hash. `add_accept` is idempotent set-add; `remove_accept` requires a referenced criterion hash and is set-remove. Adds and removes are commutative; on tie at the same hash, the op with greater HLC wins (remove-wins for equal HLC is impossible because op_hashes differ). |
| `deps` | Grow-shrink set keyed by `(target_id, edge_type)`. `add_dep` is set-add; `remove_dep` is set-remove. Same tie semantics as acceptance. |
| `closed_at`, `closed_reason` | Set when status becomes `closed`; LWW thereafter, but only writable by ops whose payload also carries `status=closed`. |
| `redact` | Tombstone-style: marks named fields for redaction. Subsequent fold renders those fields as `"<redacted>"`. Never mutates prior payloads on disk. |
| `delete` | Issue-level tombstone. After applying, all subsequent reads of the issue return only the tombstone marker; further ops on that `issue_id` are parsed but ignored for state (still counted for compaction stats). |

Every per-field LWW comparison uses the full tuple `(wall, logical, op_hash)`;
ties on `(wall, logical)` are resolved by lexicographic `op_hash`. `node_id`
is *not* used for tiebreaking — it is already mixed into `op_hash`.

### 4. Atomic claim protocol

`act update <id> --claim` and the bare `act claim <id>` execute exactly:

```
1. Generate claim op locally:
     op = { op_type: "claim", issue_id, payload: {assignee: $user},
            hlc: send(now, last_hlc), node_id, schema_version, writer_version }
   Write to .act/ops/<id>/<yyyy-mm>/<iso>-<op_hash[0:4]>-claim.json.

2. git add <op_file>
   git commit --no-verify -m "act-op: <id> claim"

3. If not --isolated:
     git pull --rebase <remote> <branch>
   (If rebase conflicts on .act/ops/** the run aborts with E_REBASE; ops
    files are append-only so textual conflicts indicate corruption.)

4. Re-fold the issue from the post-rebase tree.

5. Determine winner: among all claim ops visible for this issue with
   no intervening close/reopen, pick the one with the smallest
   (hlc.wall, hlc.logical, op_hash). That op's assignee is the winner.

   If winner.op_hash == our_op_hash:
     if --push: git push
     emit {"claimed": true, "issue": <id>, "op_hash": <hash>}
     exit 0
   else:
     emit {"claimed": false, "winner": <assignee>,
           "your_op_hash": <hash>, "winning_op_hash": <hash>}
     exit 1
```

Note the winner is the *earliest* claim, not the latest: claim is a race to
file first, and the HLC tuple determines order globally.

`--wait` retries the full sequence on loss with backoff `1s, 2s, 4s, 8s, ...`
capped at `--wait-timeout` (default 30s, max 600s). Each retry re-issues a
fresh claim op with a fresh HLC; prior losing ops remain in history but are
ignored once a different writer's claim has won and is unrebutted by a close.

### 5. Fold checkpoint

`.act/fold-checkpoint.json` is a derived cache:

```json
{
  "schema_version": 1,
  "tree_hash": "<git-tree-sha-of-.act/ops>",
  "issues": {
    "act-a1b2...": {
      "subtree_hash": "<git-tree-sha-of-.act/ops/act-a1b2>",
      "fold_hash": "<sha256 of canonical_json(issue_state)>"
    }
  }
}
```

Reuse rule:

```
load_or_fold():
  cp = read_checkpoint()
  current = git_tree_hash(".act/ops")
  if cp.tree_hash == current:
    return cached_fold(cp)            # full hit; SQLite index reused
  else:
    for id in known_issues + new_issues:
      cur_sub = git_tree_hash(".act/ops/" + id)
      if cp.issues[id].subtree_hash == cur_sub:
        keep cached fold for id
      else:
        refold id from disk
    write new checkpoint with refreshed tree_hash and per-id entries
```

`git_tree_hash(path)` is the literal git tree SHA-1 of the path as committed
(or, for unstaged changes, computed via `git ls-files -s` + `git mktree`).
This makes subtree hashes invariant under squash/rebase, matching the
property the doctor relies on for closed-by-tree verification.

### 6. Determinism contract

For any set of op files `S` (regardless of filesystem order, regardless of
when each file was created, regardless of which machine wrote which op):

> `fold(S)` produces byte-identical canonical-JSON output for every issue
> and for `act ready`, `act list`, `act show`, and `act log` results.

SQLite file bytes are explicitly **not** reproducible (page layout, free
list, rowid allocation are unspecified). Query results derived from SQLite
are. CI enforces:

1. **Permutation test.** Generate random op sets of size 1..200, shuffle into
   100 random orderings, fold each, assert all 100 fold outputs are
   byte-identical.
2. **Cross-platform test.** Same op set folded on macOS-arm, Linux-amd64,
   Windows produces byte-identical JSON.
3. **Checkpoint-vs-cold test.** `fold(S)` from a cold cache equals
   `fold(S)` from a stale checkpoint that recomputes only changed subtrees.
4. **Replay test.** `act log <id>` followed by re-importing those ops into
   an empty repo (with HLC reassignment via the importer) produces an issue
   whose post-import canonical state matches the source modulo id-mapping.
5. **HLC monotonicity test.** Across 10k random op sequences, no fold ever
   observes `last_hlc` decrease for a given `(issue_id, field)`.

A failure of any of these is a release blocker; `act doctor --check
fold-determinism` exposes (1) and (3) for local invocation.

## Commands and MCP surface

This section specifies every v1 CLI command, the universal flag set, exit codes, JSON output schemas, side effects, edge cases, and the MCP tool surface that wraps them. Commands marked agent-relevant default to JSON output when invoked with `--json`; without `--json` they emit a stable but not-load-bearing human-readable form. Every write command shares the auto-commit/push flag set defined under "Universal flags".

### Universal flags (write commands)

These flags are accepted by every command that produces a new op (`create`, `update`, `close`, `dep add`, `init`):

- `--no-commit` — write the op file and stage it; do not run `git commit`. Useful for batching agents.
- `--push` — after the op-commit, run `git push` to the configured remote. Implies a successful local commit.
- `--isolated` — local-only mode. Skips remote fetch (relevant for `--claim`'s pull-rebase step) and refuses `--push`. Mutually exclusive with `--push`.
- `--verify` — run host git hooks during the op-commit. Default is `--no-verify` (op commits touch only `.act/ops/**`).

Precedence rules:
1. `--no-commit` + `--push` is a usage error (exit 2): cannot push what wasn't committed.
2. `--isolated` + `--push` is a usage error (exit 2): isolated forbids network.
3. `--verify` + `--no-commit` is silently a no-op (no commit happens, hooks irrelevant).
4. `--claim` (on `update`) implicitly fetches unless `--isolated` is set.

Auto-publish on write (Phase 2, act-65a7d5). When the nested `.act/` repo has `origin` configured, every successful auto-commit on a write subcommand (`create`, `update`, `close`, `dep add`, `reopen`, `delete`) is followed by a synchronous `git push` via the retry helper documented in §"push retry". `--push` becomes redundant in that case; setting it is harmless. No-origin repos skip the publish step silently — the op log stays local-only without ceremony. On retry exhaustion the command exits 4 with envelope `push_exhausted`; on a non-recoverable fetch failure during the retry loop, the command exits 4 with envelope `remote_unreachable`. The local commit is never rolled back on push failure: the op file is on disk and is recoverable via the harvest path.

### Offline mode (Phase 2, act-4a604d)

The universal `--offline` flag is accepted by every write subcommand (`create`, `update`, `close`, `dep add`, `reopen`, `delete`). When set, the command commits locally as usual but skips the synchronous push that ticket 3a wired. Instead, the local commit's SHA is appended to `.act/.pending-pushes` and the next non-offline write flushes the deferred publish before its own push.

Distinction from `--isolated`: `--isolated` is the strict local-only mode (no fetch, no push, no pending-push tracking) used by `--claim` and importer tooling. `--offline` is the lighter "skip push but remember to push later" mode — the commit lands locally and the next non-offline write catches up automatically.

`--offline` + `--push` is a usage error (exit 2): the two are direct opposites. `--offline` + `--no-commit` is silently reduced to `--no-commit` semantics (no commit happens, so there's nothing to defer).

**Pinned `.act/.pending-pushes` schema.** JSON-lines, one record per line, newline-terminated. Each record has exactly three fields:

```json
{"timestamp": "2026-05-19T14:23:01.234Z", "sha": "abc123…", "op_type": "create"}
```

Fields:
- `timestamp` — RFC3339 with millisecond precision, UTC (`Z` suffix).
- `"sha":` — full sha of the local commit that was NOT pushed.
- `op_type` — one of `create|close|update|dep_add|reopen|delete`.

The file is capped at 100 entries (rolling); appends beyond the cap drop the oldest entries. The cap is a safety bound — a healthy workflow has at most one or two entries between flushes.

### Slow-write observation (Phase 2, act-4a604d)

`actGitOps.Commit()` measures monotonic time between the stage point (after `git add` for the op file) and the return from `git commit`. If the elapsed duration exceeds `act.slowWriteThresholdMs` (default 1000ms), the command emits a single warning to stderr and appends a structured record to `.act/.slow-writes`.

**Pinned stderr warning text.** The literal pattern emitted on a slow write:

```
act: slow write detected (1247ms > 1000ms threshold); see .act/.slow-writes
```

The exact substrings `act: slow write detected (` and `; see .act/.slow-writes` are load-bearing and asserted by `TestDocClaim_SlowWrite_WarningText`.

**Pinned `.act/.slow-writes` schema.** JSON-lines, one record per line, newline-terminated:

```json
{"timestamp": "2026-05-19T14:23:01.234Z", "op_id": "act-abc123def456", "duration_ms": 1247, "op_type": "create"}
```

Fields:
- `timestamp` — RFC3339 with millisecond precision, UTC (`Z` suffix).
- `op_id` — full id of the op being committed (the op the slow commit is for).
- `"duration_ms":` — integer milliseconds, monotonic delta between stage and commit.
- `op_type` — one of `create|close|update|dep_add|reopen|delete` (matches the op-envelope `op_type` enum).

The file is capped at 100 entries (rolling); the cap is the same as `.act/.pending-pushes`. Doctor's case-(g) summary (ticket 9) reads this file directly with no transform.

**Fault-injection hook.** `ACT_TEST_SLOW_COMMIT_MS=<n>` is a test-only env var that injects a `time.Sleep(<n>ms)` in the commit path between the stage point and the git commit invocation. Tests use this to drive the slow path deterministically without depending on disk/git latency. Documented in `internal/gitops/gitops.go` adjacent to the slow-write measurement code.

### Universal exit codes

- `0` — success.
- `1` — logical failure: claim loss, doctor finding, cycle refused, dependency dangling, etc. The command ran correctly but the answer is "no."
- `2` — usage error: bad flag combination, unknown flag, missing required argument, ambiguous id prefix.
- `3` — environment error: not in a git repo, `.act/` missing, missing `node_id`, unwritable index.
- `4` — version skew: any op carries `writer_version > self.writer_version` ("upgrade required"). Always non-recoverable from the binary's side.

All non-zero exits emit `{"error": {"code": <int>, "kind": "<slug>", "message": "..."}}` on stdout when `--json` is set; on stderr in human mode.

### Pre-import id resolution

Every command accepting an `<id>` argument resolves the input through this pipeline, in order, and uses the first hit:

1. **Import maps**: scan `.act/imports/*.json` (sorted by filename, descending) for the input as a key in any `mapping` object. If found, replace with the mapped post-import id and continue.
2. **Exact full id**: if the input matches a known full id, use it.
3. **Prefix match**: treat the input as a prefix and find issues whose full id starts with it. Zero matches → exit 3 ("not found"). One match → use it. Multiple matches → exit 2 with `{"error": {"kind": "ambiguous_id", "candidates": ["act-a1b2", "act-a1b9", ...]}}`.

Resolution happens before any op is written, so a write command never partially applies due to a bad id.

---

### `act init`

**Synopsis:** `act init [--force]`

**Behavior:** Creates `.act/{ops,snapshots,hooks,imports}/`, an empty `index.db`, and `config.json` carrying a freshly generated `node_id = sha256(machine-id || git-config user.email)[0:8]`, `schema_version`, `writer_version`, and an empty `fold-checkpoint` placeholder.

**Flags:**
- `--force` (bool, default false) — proceed even if `.act/` already exists; merges missing subdirs and rewrites `config.json` only if absent. Never overwrites existing ops or snapshots.

**Exit codes:** 0 on success; 2 if `.act/` exists and `--force` not given; 3 if `cwd` is not inside a git working tree.

**JSON output (`--json` always emitted on init):**
```json
{"ok": true, "act_dir": "/abs/path/.act", "node_id": "7f3a91c2",
 "writer_version": "0.1.0", "schema_version": 1}
```

**Side effects:** Creates directories and files under `.act/`. Does NOT create a commit; `init` is the one write that defers commit to the user's first real op.

**Edge cases:** bare git repo → exit 3 (no working tree); submodule vs superproject scopes to the nearest `.git`; `--force` on a `.act/` with existing ops leaves ops untouched and only patches missing dirs.

---

### `act create <title>`

**Synopsis:** `act create <title> [-p N] [--parent ID] [--accept "criteria"]... [--type T] [--description "text"] [--json]` plus universal flags.

**Flags:**
- `-p, --priority N` (int 0..3, default 2). 0 is highest.
- `--parent ID` (string, optional). Resolved via id-resolution pipeline.
- `--accept "criteria"` (string, repeatable). Each invocation appends one acceptance-criterion string.
- `--type T` (enum task|bug|epic|chore, default task).
- `--description "text"` (string, default "").
- `--json` (bool, default false for humans, true under MCP).

**Behavior:** Builds a `create` op payload, hashes `(payload || nonce)` to derive the new issue id `act-<N hex>` where `4 <= N <= 16` (shortest non-colliding prefix per §ID model), writes the op file at `.act/ops/<id>/<yyyy-mm>/<iso>-<hash6>-create.json`, runs the `post-create` hook, then op-commits unless `--no-commit`.

**JSON output:**
```json
{"ok": true, "id": "act-a1b2c3d4e5f60718", "prefix": "act-a1b2",
 "op_id": "7f3a...", "committed": true, "pushed": false}
```

**Exit codes:** 0; 1 if hook rejects; 2 on bad flags or unknown parent; 3 if `.act/` missing; 4 on writer-version skew encountered during the implicit fold.

**Edge cases:** `--parent` to a closed/redacted issue is allowed with a stderr warning; empty title or title >256 bytes → exit 2; hash collision with an existing id retries the nonce up to 8 times before exit 1.

---

### `act list`

**Synopsis:** `act list [--status X] [--assignee Y] [--type T] [--json] [--limit N] [--sort field]`

**Flags:**
- `--status X` (csv string, e.g. `open,in_progress`). Default: all non-closed.
- `--assignee Y` (string, exact match). Default: any.
- `--type T` (enum). Default: any.
- `--json` (bool).
- `--limit N` (int, default 200).
- `--sort field` (enum priority|created_at|closed_at|id, optionally suffixed `:asc`/`:desc`). Default sort order: priority asc, then created_at desc. Tie-breaker: id asc.

**Behavior:** Reads from `.act/index.db` after a fold-checkpoint validation. Index is rebuilt automatically if the tree-hash mismatches.

**JSON output:**
```json
{"ok": true, "count": 12, "issues": [
  {"id":"act-a1b2c3d4e5f60718","prefix":"act-a1b2","title":"...",
   "status":"open","priority":1,"type":"task","assignee":"agent-1",
   "parent":null,"created_at":"2026-04-29T14:23:01Z"}
]}
```

**Exit codes:** 0; 2 on bad flag; 3 if `.act/` missing; 4 on version skew during rebuild.

**Edge cases:** empty result returns `count: 0` and exit 0; `--limit 0` or unknown sort field → exit 2.

---

### `act show <id>`

**Synopsis:** `act show <id> [--json] [--include-ops]`

**Flags:**
- `--json` (bool).
- `--include-ops` (bool, default false). Inlines the full op stream alongside the folded snapshot.

**Behavior:** Resolves id (errors with exit 2 on prefix ambiguity), folds the issue, prints snapshot.

**JSON output:**
```json
{"ok": true, "issue": {
  "id":"act-a1b2c3d4e5f60718","title":"...","description":"...",
  "status":"open","priority":1,"type":"task","parent":null,
  "deps":[{"id":"act-9c2b...","type":"blocks"}],
  "assignee":"agent-1","acceptance_criteria":["..."],
  "created_at":"...","closed_at":null,"closed_reason":null,
  "ops_count": 4
}, "ops": [/* present iff --include-ops */]}
```

**Exit codes:** 0; 2 on ambiguous prefix or unknown flag; 3 if id not found or `.act/` missing; 4 on writer-version skew.

---

### `act update <id>`

**Synopsis:** `act update <id> [--status X] [--priority N] [--assignee Y] [--description T] [--accept "..."] [--dep-rm ID] [--claim] [--json] [--wait] [--wait-timeout SECS]` plus universal flags.

**Flags:**
- `--status X` (enum open|in_progress|blocked|closed). `closed` requires `act close` instead — exit 2.
- `--priority N` (int 0..3).
- `--assignee Y` (string; empty string clears).
- `--description T` (string).
- `--accept "..."` (repeatable; each appends one criterion).
- `--dep-rm ID` (repeatable). Removes a dep edge; resolves id.
- `--claim` (bool). Atomic claim protocol: write `claim` op, `git pull --rebase` (skipped under `--isolated`), refold, report win/loss. On win, optionally push if `--push`.
- `--wait` (bool, only with `--claim`). On loss, poll until the issue becomes claimable again or status becomes terminal.
- `--wait-timeout SECS` (int, default 60). Bound on `--wait`.
- `--json` (bool).

Each non-`--claim` field flag generates one op (so `--status blocked --priority 0` writes two ops in one commit).

**JSON output (claim win):**
```json
{"ok": true, "claimed": true, "id":"act-a1b2c3d4e5f60718",
 "winner":"7f3a91c2","ops_written":["claim","set-priority"]}
```
**JSON output (claim loss):**
```json
{"ok": false, "claimed": false, "id":"act-a1b2c3d4e5f60718",
 "winner":"9c2bdead","reason":"lost-race"}
```
Exit codes for `--claim`: `0` win, `1` loss or other logical error, `2` usage. Other update modes follow universal exit codes.

**Edge cases:** `--claim` with no remote configured warns on stderr and proceeds local-only; HLC drift >5min from repo reference refuses the op (exit 1); `--dep-rm` of a non-existent edge → exit 1 (logical, not usage); `--wait` without `--claim` → exit 2.

---

### `act close <id>`

**Synopsis:** `act close <id> [--reason TEXT] [--json]` plus universal flags.

**Flags:**
- `--reason TEXT` (string, default ""). Stored as `closed_reason`.
- `--json` (bool).

**Behavior:** Writes a `close` op carrying `closed_at` and `closed_reason`; computes and stores the `closed_by_tree` reverse index (git tree hash of `.act/ops/<id>/`); runs `post-close` hook; commits with message `act-op: <id> close`.

**JSON output:**
```json
{"ok": true, "id":"act-a1b2c3d4e5f60718", "closed_at":"...",
 "closed_reason":"shipped", "committed":true, "pushed":false}
```

**Exit codes:** 0; 1 if already closed (idempotent close re-emits no op and exits 0; only true conflict returns 1); 2 on bad flags; 3 missing `.act/`; 4 on skew.

**Edge cases:** closing an issue with open children is allowed and surfaced by `doctor orphan-close`; reason >4KB → exit 2.

---

### `act dep add <child> <parent>`

**Synopsis:** `act dep add <child> <parent> [--type T] [--json]` plus universal flags.

**Flags:**
- `--type T` (enum blocks|relates|supersedes, default blocks).
- `--json` (bool).

**Behavior:** Resolves both ids, refuses if it would create a cycle in the `blocks` subgraph (cycle detection runs over the full folded graph). Writes a `dep-add` op.

**JSON output:**
```json
{"ok": true, "child":"act-a1b2...", "parent":"act-9c2b...",
 "type":"blocks", "committed":true}
```

**Exit codes:** 0; 1 on cycle (`{"error":{"kind":"cycle","path":[...]}}`); 2 on bad flags; 3 on missing id.

**Edge cases:** self-edge → exit 2; duplicate edge is idempotent (no op, exit 0); `relates`/`supersedes` are not cycle-checked.

---

### `act ready`

**Synopsis:** `act ready [--under <id>] [--json] [--limit N]`

**Flags:**
- `--under <id>` (string, optional). Restrict output to descendants (via `parent` edges) of this id.
- `--json` (bool).
- `--limit N` (int, default 50).

**Algorithm:** Fold all issues; an issue is **ready** iff `status == open` AND no incoming `blocks` dep points at it from an open/in_progress issue. Sort by priority asc, created_at desc, id asc.

**JSON output:**
```json
{"ok": true, "count": 3, "issues": [{"id":"...","prefix":"...","title":"...","priority":0,"type":"task"}]}
```

**Exit codes:** 0; 2 bad flags; 3 missing `.act/`; 4 on skew.

---

### `act search <query>`

**Synopsis:** `act search <query> [--in title|desc|all] [--status X] [--limit N] [--json]`

**Flags:**
- `--in` (enum, default `all`). Restrict FTS5 column scope.
- `--status X` (csv).
- `--limit N` (int, default 50).
- `--json` (bool).

**Behavior:** SQLite FTS5 query; index rebuilt-on-demand if stale.

**JSON output:**
```json
{"ok": true, "count": 5, "results":[
  {"id":"...","prefix":"...","title":"...","snippet":"...","rank":-3.41}
]}
```

**Exit codes:** 0; 2 on bad query syntax (FTS5 parse error surfaced as usage); 3 missing index; 4 on skew.

---

### `act log <id>`

**Synopsis:** `act log <id> [--json]`

**Behavior:** Resolves id; emits the chronological op stream (HLC-sorted) for the issue. Includes `op_id`, `op_type`, `payload`, `hlc`, `node_id`, `writer_version`, and the file path.

**JSON output:**
```json
{"ok": true, "id":"act-a1b2c3d4e5f60718", "ops":[
  {"op_id":"7f3a...","op_type":"create","hlc":"...","node_id":"...",
   "writer_version":"0.1.0","payload":{...},"path":".act/ops/act-a1b2.../..."}
]}
```

**Exit codes:** 0; 2 on ambiguous prefix; 3 on unknown id; 4 on skew.

---

### `act doctor`

**Synopsis:** `act doctor [--check NAME] [--fix] [--json] [--compact]`

**Flags:**
- `--check NAME` (repeatable). Default: run all.
- `--fix` (bool). Auto-remediate where safe (per check, below).
- `--json` (bool).
- `--compact` (bool). Manual escape hatch to compact eligible issues.

**Checks and `--fix` behavior:**

| Check | Algorithm | `--fix` effect |
|---|---|---|
| `orphan-close` | Find issues with `closed_at` set but no commit message containing `(act-<prefix>)` AND no diff in `.act/ops/<id>/` at close time. | Read-only; surfaces finding. |
| `orphan-ops` | Op files referencing an `issue_id` that has no `create` op. | Read-only. |
| `dangling-deps` | Dep edges pointing at unknown ids (post-resolution). | Read-only. |
| `time-travel` | Adjacent ops with HLC going backward more than the 5-minute drift bound. | Read-only. |
| `cycle` | Cycle in the `blocks` subgraph. | Read-only. |
| `unknown-op-version` | Any op with `writer_version > self`. | Cannot fix; exits 4. |
| `index-divergence` | Recompute index into a tmp SQLite from ops/snapshots; row-by-row diff against `.act/index.db` (recomputed = oracle). | Replaces `.act/index.db` with the recomputed db. |
| `index-schema` | Compare index schema version to expected. | Drops and rebuilds `.act/index.db`. |

**JSON output:**
```json
{"ok": true|false, "checks":[
  {"name":"cycle","status":"pass|fail","findings":[...],"fixed":false}
], "summary":{"pass":7,"fail":1}}
```

**Exit codes:** 0 on all-pass; 1 if any check fails (and `--fix` did not remediate); 4 on unknown-op-version; 2 on bad flags.

---

### `act mcp`

**Synopsis:** `act mcp [--read-only] [--workdir DIR]`

**Flags:**
- `--read-only` (bool). Server-level: refuses any write tool call regardless of per-call `read_only`.
- `--workdir DIR` (string). Chdir before serving; required when launched outside the repo.

**Behavior:** Stdio MCP server, no network. Tool surface specified below.

**Exit codes:** 0 on clean shutdown; 2 bad flag; 3 missing `.act/`; 4 on skew.

---

### `act version`

**Synopsis:** `act version [--check-repo] [--json]`

**Flags:**
- `--check-repo` (bool). Walk all op files to find `max(writer_version)`; compare to `self.writer_version`. Exits 4 if `self < max`.
- `--json` (bool).

**JSON output:**
```json
{"ok": true, "binary_version":"0.1.0", "writer_version":"0.1.0",
 "schema_version":1, "repo_max_writer_version":"0.1.0"}
```

**Exit codes:** 0; 4 on skew (with `--check-repo`); 3 on missing `.act/` (with `--check-repo` only).

---

### MCP tool surface

The server exposes one tool per CLI command, named `act_<verb>` (`act_init`, `act_create`, `act_list`, `act_show`, `act_update`, `act_close`, `act_dep_add`, `act_ready`, `act_search`, `act_log`, `act_doctor`, `act_version`), plus three composed tools. All tools accept a `read_only: bool` field; the server rejects any write tool when started with `--read-only` regardless of this field. Transport is stdio. JSON Schema (concise) for each:

**Per-command tools.** Each `act_<verb>` accepts an object whose fields mirror the CLI flags (kebab-case becomes snake_case). Output is the command's `--json` body. Errors surface as MCP tool errors carrying `{code, kind, message}`.

**Composed tool: `act_next`**

Input:
```json
{"type":"object","properties":{
  "under":{"type":"string"},
  "read_only":{"type":"boolean"}
}}
```
Behavior: `ready` → claim first candidate → `show`. On claim loss, exponential backoff: 3 attempts at 100ms, 400ms, 1.6s with ±25% jitter; refold and exclude just-lost ids each attempt. On exhaustion, return candidates without claiming.

Output:
```json
{"oneOf":[
  {"properties":{"claimed":{"const":true},"issue":{"$ref":"#/issue"}}},
  {"properties":{"claimed":{"const":false},"candidates":{"type":"array"}}}
]}
```

**Composed tool: `act_finish`**

Input: `{"id":"string","reason":{"type":"string"}}`. Behavior: `act close --reason ...`; commit message includes the prefix marker `(act-XXXX)` so `act doctor orphan-close` can grep it. Output: same as `act close`.

**Composed tool: `act_block`**

Input: `{"id":"string","blocked_by":"string","reason":{"type":"string"}}`. Behavior: write `set-status=blocked` op, then `dep-add` with `type=blocks` from `id` to `blocked_by`. Output:
```json
{"ok":true,"id":"...","blocked_by":"...","ops_written":["set-status","dep-add"]}
```

All composed tools mark themselves as the recommended path in their tool descriptions, surfacing `act_ready`/`act_update --claim`/`act_close`/`act_dep_add` as escape hatches.

## Errors, hooks, migration, bootstrap, compaction, tests

This section closes the remaining surfaces: structured error taxonomy, hook execution contract, bootstrap importer, op-schema migration, opportunistic compaction, edge-case behaviors, and the test plan. Together with the prior sections it makes the v1 surface implementable from one document.

### 1. Error handling

Every `act` command exits with a small, stable error code string. Under `--json`, errors are emitted on stdout as a single object; without `--json`, a one-line human message goes to stderr and stdout stays empty. JSON shape is uniform:

```
{"error": "<code>", "message": "<human>", "details": {...}}
```

`details` is optional and per-class. Exit codes are stable; new classes get new codes, never re-used.

| code                       | exit | human message format                                                       | details keys                                  |
|----------------------------|------|----------------------------------------------------------------------------|-----------------------------------------------|
| `not_in_git`               | 2    | `not in a git repository (run from inside a git worktree)`                 | `cwd`                                         |
| `act_not_initialized`      | 2    | `.act/ not found in <root>; run 'act init'`                                | `repo_root`                                   |
| `issue_not_found`          | 3    | `no issue matching '<query>'`                                              | `query`                                       |
| `id_ambiguous`             | 3    | `prefix '<p>' is ambiguous: <id1>, <id2>, ...`                             | `prefix`, `candidates[]`                      |
| `version_skew`             | 4    | `op writer_version <N> exceeds binary max <M>; upgrade act`                | `op_writer_version`, `binary_max`, `op_path`  |
| `claim_lost`               | 5    | `claim lost; winner=<node_id> at hlc=<hlc>`                                | `winner_node_id`, `winner_hlc`, `issue_id`    |
| `cycle_detected`           | 6    | `dep would create cycle: <a> -> ... -> <a>`                                | `path[]`                                      |
| `dep_not_found`            | 6    | `dep target '<id>' not found`                                              | `target`                                      |
| `hook_failed`              | 7    | `hook '<name>' exited <rc>; op unstaged`                                   | `hook`, `exit_code`, `stderr_tail`            |
| `op_invalid`               | 8    | `op rejected: <reason>`                                                    | `reason`, `op_path`                           |
| `hlc_drift`                | 8    | `hlc drift exceeds 5m: op=<hlc> ref=<ref>`                                 | `op_hlc`, `ref_hlc`, `delta_seconds`          |
| `index_corrupt`            | 9    | `index.db corrupt or schema-mismatched; rerun 'act doctor --rebuild'`      | `reason`                                      |
| `import_invalid_jsonl`     | 10   | `bootstrap line <N> rejected: <reason>`                                    | `line`, `reason`, `source`                    |
| `compaction_locked`        | 0    | `compaction already in progress; skipping` (warning, exit 0)               | `lock_path`                                   |
| `redact_target_not_found`  | 3    | `redact target '<field>' not present on <id>`                              | `issue_id`, `field`                           |
| `push_exhausted`           | 4    | `push retries exhausted after <N> attempts; last error: <msg>`             | `retry_count`, `shallow_unshallow_attempted`, `last_error` |
| `remote_unreachable`       | 4    | `git fetch failed: <reason>`                                               | `remote`, `branch`, `stderr_tail`             |
| `bootstrap_timeout`        | 4    | `act bootstrap-worker: clone <url> exceeded <N>s budget`                   | `timeout_seconds`, `url`                      |
| `target_not_empty`         | 2    | `act bootstrap-worker: <path> exists and is non-empty; pass --force to overwrite` | `target`                                |

Rules:
- Every command MUST return exactly one error class on failure; no compound errors.
- `details` MUST be deterministic given inputs (no timestamps, no PIDs) so test goldens are stable.
- Human messages are single-line, no ANSI, no trailing period, lowercase first word except proper identifiers.
- `--json` errors go to stdout because agents parse stdout; stderr stays empty under `--json`.

### 2. Hooks contract

Discovery: only three filenames are loaded.

```
.act/hooks/post-create
.act/hooks/post-close
.act/hooks/post-claim
```

Any other filename in `.act/hooks/` is ignored. Future phases (`pre-commit-op`, `post-fold`, `post-compact`) are reserved names — `act` MUST refuse to load them in v1 with no warning, so a future binary can adopt them without breaking older repos.

Execution:
1. Op file is written and `git add`-staged.
2. `act` resolves the hook by op-type (`create`/`close`/`claim`) and stats it. If absent or not executable, the hook is skipped silently.
3. Hook is spawned synchronously with cwd = repo root.
4. Stdin: the op JSON, exactly one line, no trailing newline. Closing stdin signals EOF.
5. Env vars set: `ACT_OP_ID`, `ACT_OP_TYPE`, `ACT_ISSUE_ID`, `ACT_HOOK_PHASE=pre-commit-op`. No other `ACT_*` vars are set; future vars are additive.
6. Stdout and stderr are fully captured (bounded to 64KB each; overflow truncates and tags `details.truncated=true`). On success they are discarded; on failure stderr_tail (last 4KB) is included in the `hook_failed` error.
7. Timeout: 5s wall-clock. On timeout `act` sends SIGTERM, waits 1s grace, then SIGKILL. Either way the hook is treated as failed.
8. Non-zero exit: `act` runs `git restore --staged <op-path>`, deletes the op file, and emits `hook_failed`. No commit is created. Caller exits with code 7.

Hooks NEVER run on: `act fold` (read-only), replay/recovery, `act import`, fresh `git clone` (the ops already exist in history; the writer that produced them already ran their hook). The invariant is "hooks fire exactly once per logical op, on the writer that originated it".

### 3. Bootstrap importer

Input: a single JSONL file (default `_generated/projects/act/issues.jsonl`). Each line is one op envelope:

```
{"op_version": 1, "schema_version": 1, "op_type": "create",
 "issue_id": "bootstrap-7", "payload": {...}, "hlc": "...", "node_id": "..."}
```

Steps (in order, all-or-nothing):
1. **Validate.** Read the file; for each line, parse JSON, check required keys, check `op_type` is in the known set, check `payload` shape against the op-type schema. Any failure → `import_invalid_jsonl` with the offending line number. No side effects yet.
2. **Replay.** For each validated line, write a fresh local op via the normal write path: importer issues a new HLC (monotonic from local clock), uses the local `node_id` from `.act/config.json`, and produces a new act-style id (`act-<8hex>`). The bootstrap `issue_id` is recorded in a mapping table; subsequent ops in the same input that reference the bootstrap id are rewritten to the new local id before being written.
3. **Mapping.** Write `.act/imports/<iso-utc>.json`:
   ```
   {"source": "issues.jsonl@<sha256-of-input>",
    "mapping": {"bootstrap-7": "act-9c2b...", ...},
    "imported_at_hlc": "<hlc>"}
   ```
4. **Single commit.** All imported op files plus the mapping file are committed in one `git commit` with message `act-import: <source-sha-short> <count> ops`. Hooks do NOT fire (rule from §2).

Idempotency: before step 2, the importer scans `.act/imports/*.json` for any file whose `source` sha matches the input. If found, the importer exits 0 with `{"imported": 0, "reason": "already_imported"}`. Re-running with the same input is a strict no-op.

Resolution: `act show <id>` and `act log <id>` accept either a bootstrap id or a local act id. Lookup walks `.act/imports/*.json` mappings in lexicographic order; first hit wins. Bootstrap ids are never re-used across imports.

Unknown `op_type` in the input is rejected at step 1 with `import_invalid_jsonl`. There is no quarantine path; the operator fixes the JSONL and re-runs.

### 4. Op-schema migration

Two version axes live in every op payload:
- `op_version` — increments when an op-type's payload shape changes.
- `schema_version` — increments when the issue state schema (the folded shape) changes.
- `writer_version` — the binary that produced the op; reader gate.

Reader gate: on read, if `op.writer_version > binary.max_known_writer_version`, the binary refuses with `version_skew`. This is a hard failure, not a warning; it forces upgrade rather than silent miscomputation.

Fold dispatch: the fold loop calls

```
state = apply_op(state, op, op.op_version)
```

`apply_op` is a registry keyed by `(op_type, op_version)`. Each entry is a pure function. Adding a new op_version means adding a new registry entry; old entries stay forever so historical ops keep folding identically.

Migration ops: `act migrate` is the only command that writes a `migrate` op. Its payload:

```
{"op_type": "migrate",
 "from": {"op_version": 1, "schema_version": 1},
 "to":   {"op_version": 2, "schema_version": 2},
 "transform": "<named-transform-id>",
 "applies_to_issue": "<id>",
 "writer_version": "<binary-semver>"}
```

One `migrate` op per affected issue (not one global). The transform is referenced by name, not embedded as code; the binary that wrote the migrate op is the authority for what that name means at that `writer_version`. Replays on newer binaries follow the registry. Older binaries hit `version_skew` and refuse — correct behavior.

`act migrate --dry-run` lists affected issues and the transform that would apply, JSON output, no writes.

### 5. Compaction

Trigger conditions (any writer detects on its way out of a successful op):
- Issue has > 50 ops in `.act/ops/<id>/`, OR
- > 30 days since the last `compact` op for the issue (or no `compact` op ever and the issue is > 30 days old).

Procedure:
1. **Acquire lock.** `flock` on `.act/.compact.lock` (created if absent), non-blocking. Failure → emit `compaction_locked` warning to stderr (exit 0) and return; the next writer will retry.
2. **Re-fold.** Build the issue state from current ops on disk.
3. **Snapshot file.** Write `.act/snapshots/<id>.json`:
   ```
   {"id": "<id>",
    "state": {...folded fields...},
    "subsumed_ops": [".act/ops/<id>/2026-04/...json", ...],
    "as_of_hlc": "<hlc>",
    "tree_hash": "<git-tree-hash-of-ops-dir-at-snapshot-time>"}
   ```
4. **Optional prune.** If the issue is closed and `closed_at` is > 30 days ago, delete the subsumed op files. Otherwise leave them; the snapshot is an accelerator, not a replacement.
5. **Compact op.** Write a `compact` op at `.act/ops/<id>/<yyyy-mm>/...-compact.json`:
   ```
   {"op_type": "compact",
    "snapshot_path": ".act/snapshots/<id>.json",
    "snapshot_tree_hash": "<sha>",
    "subsumed_count": N}
   ```
6. **Commit.** One commit: snapshot file + compact op (+ deletions if step 4 ran). Message: `act-op: <id> compact`. `--no-verify` like other op commits.
7. **Release lock.** `flock` released on process exit; explicit unlock on success.

Concurrency: contention skips silently. Two writers will not both compact the same issue in the same second; the loser's trigger will re-fire on its next op.

Fold behavior post-compaction: when a snapshot exists and the snapshot's `tree_hash` matches the current tree-hash of the ops dir up through `as_of_hlc`, the fold seeds from the snapshot and applies only ops with `hlc > as_of_hlc`. Hash mismatch falls back to full fold and logs an `index-divergence` finding for `act doctor`.

### 6. Edge cases

- **Empty repo on `act init`.** Creates `.act/` with `config.json` (node_id, version), empty `ops/`, empty `snapshots/`, empty `hooks/`, empty `imports/`. Stages and commits in one commit: `act-init: <node_id>`. No-op if `.act/` already exists; exit 0 with `{"initialized": false}`.
- **`.act/ops/` missing on first run after init.** Treated as zero ops; fold returns empty state. No error.
- **Importer encounters unknown `op_type`.** Step-1 validation fails → `import_invalid_jsonl`. The whole import is aborted before any op file is written.
- **Two terminals on the same machine claim the same issue.** Both write distinct claim-op files (filenames include op-payload hash and node_id, so the two ops have different filenames even with identical wall-clock seconds because of payload nonce). Fold orders by HLC then op-hash; one wins, the other observes `claim_lost`.
- **`act close` on an already-closed issue.** Idempotent. The op is still written (audit trail), but fold sees status already `closed` and the post-state is unchanged. Exit 0 with `{"changed": false}`.
- **Concurrent `act create` of the same title.** IDs derive from `(payload + nonce)`, never from title. Two creates produce two distinct ids; no conflict, no error. Operators sort it out via dep edges later.
- **Redact of an already-redacted field.** Idempotent. A second redact op is written; fold output unchanged. Exit 0 with `{"changed": false}`.
- **Prefix collision at create time.** The new id is checked against all existing ids. If the chosen 4-hex prefix already maps to a different id, `act` extends the displayed prefix length one hex at a time until unique, and stores the longer form in the per-session prefix map. The full id is unchanged.
- **Squash-and-push refused on `version_skew`.** When `act` would squash a contiguous `act-op:` range and any op in the range has `writer_version > binary.max_known_writer_version`, it refuses with `version_skew` and prints: `upgrade act to >= <op_writer_version> before pushing; affected commits: <list>`. No partial squash.
- **`act doctor --check unknown-op-version`** emits a finding per offending op with the same details payload, so the operator can correlate before attempting a push.

### 7. Test plan

Test code lives under `internal/.../*_test.go` (or equivalent if the Bun spike flips the language). Goldens live under `testdata/golden/`. Fuzz corpora under `testdata/fuzz/`. CI runs all tiers on every PR.

#### 7.1 Property tests

- **`prop_fold_permutation_invariance`** — Generator produces a set of commutative-disjoint ops (different issues OR different fields on the same issue). For every permutation of the set, fold produces identical state JSON. Asserted via byte-equality after canonical JSON serialization.
- **`prop_hlc_monotonicity`** — For any sequence of ops emitted by a single node within one process, the HLC strictly increases. Generator produces synthetic local-clock skews including backward jumps; the property holds across them.

#### 7.2 Golden tests

One file per `(op_type, op_version)` pair under `testdata/golden/<op_type>/<version>/<case>.json`. Each case has `prior_state.json`, `op.json`, `expected_state.json`. The test loads prior, applies op, compares expected. Required cases: `create`, `update-status`, `update-priority`, `update-assignee`, `update-description`, `update-accept`, `claim`, `close`, `dep-add`, `dep-rm`, `redact`, `migrate`, `compact`. Adding a new op_version requires adding a new golden directory; CI fails if the directory is missing.

#### 7.3 Fuzz

- **`fuzz_fold_determinism`** — Random op-sequence generator with shrinking. For each generated sequence, fold twice (cold and warm) and assert byte-identical canonical JSON. Shrinker minimizes failing sequence length on regression. Corpus is committed; new findings are added back to the corpus.

#### 7.4 Concurrency tests

- **`concurrent_claim_two_writers`** — Two child processes each run `act update --claim <id>` against the same issue with a shared remote bare repo. Exactly one exits 0 with `{"claimed": true}`; the other exits 5 with `claim_lost` and a winner field. Asserted across 100 iterations to catch flakes.
- **`concurrent_distinct_ops`** — Two writers update different fields (priority and description). Both ops survive after `git pull --rebase`; final state contains both updates. No error from either writer.
- **`rebase_contention`** — Three writers update the same field (priority) concurrently. After all rebases settle, fold output is deterministic across runs (HLC + op-hash tiebreaker). Asserted by running the scenario 50 times and comparing final-state hashes.

#### 7.5 MCP end-to-end

A fake stdio MCP client process spawns `act mcp` and drives `act_next`, `act_finish`, `act_block`. Asserts:
- Tool list response contains the three composed tools plus the 1:1 surface.
- `act_next` returns `{"claimed": true, "issue": {...}}` on a fresh ready queue.
- `act_next` on a contended queue returns `{"claimed": false, "candidates": [...]}` after the bounded-retry budget.
- `act_finish` writes a close op and returns `{"closed": true, "id": ...}`.
- `act_block` writes status=blocked and a dep edge atomically.

#### 7.6 Doctor coverage

For each check (`orphan-close`, `orphan-ops`, `dangling-deps`, `time-travel`, `cycle`, `unknown-op-version`, `index-divergence`, `index-schema`):
- **Positive test** — synthesize the broken state on disk, run the check, assert exactly one finding with the expected code and details.
- **Negative test** — start from a clean seeded repo, run the check, assert zero findings.

#### 7.7 Importer

- **`import_idempotent`** — Run importer twice on the same JSONL; second run exits with `{"imported": 0, "reason": "already_imported"}` and produces no new commits.
- **`import_malformed_jsonl`** — Inject a bad line (missing `op_type`); importer exits with `import_invalid_jsonl` and the affected line number; no op files written, no commit.
- **`import_mapping_determinism`** — Given fixed input bytes and a fixed local node_id + clock, the mapping file is byte-identical across runs (after wiping and re-running). Asserted via sha256 of the mapping file.

#### 7.8 CI matrix

Three containerized environments, one job each:
- **CC laptop** — Linux container approximating Claude Code on a laptop; runs install + smoke workflow (init, create, claim, close, list --json) and asserts JSON shapes via `jq` schema checks.
- **CC on the Web** — Container matching the web sandbox; same smoke workflow.
- **Cowork** — Container with the Cowork plugin manifest; drives `act mcp` over stdio and runs the MCP E2E.

All three jobs must pass. Each job posts a single PASS/FAIL summary to the run; an agent reads them and decides whether to advance the build pipeline. Human signs off only on the final tag.

## §5 Clarifications (round-1 resolutions)

### 5.A.1 Stamped op `issue_id` is the on-disk hash; display computes shortest unique prefix
The op envelope `issue_id` MUST be the on-disk stamped id from the collision-extension procedure, with a minimum length of 4 hex characters. The display layer MUST recompute the shortest-unique prefix per invocation purely for printing; the stamped id is never rewritten for display purposes.

### 5.A.2 `redact` indices refer to post-fold current index space at the redact's HLC
A `redact` op's `field_path` indices (e.g. `acceptance_criteria[2].text`) MUST be resolved against the post-fold current index space at the redact op's HLC. Folding a later `remove_accept` does not retroactively renumber prior redacts; the redact's effect MUST be recomputed against current indices on each fold.

### 5.A.3 HLC ordering uses parsed milliseconds; RFC3339 string MUST round-trip exactly
HLC ordering MUST use parsed milliseconds-since-epoch derived from the RFC3339 `hlc.wall` string. The RFC3339 string is the wire format and MUST round-trip exactly to the int64 ms value. Sub-millisecond precision is not allowed in the wire form.

### 5.A.4 `update_field status=closed|in_progress` is rejected at write time
`update_field` with `field=status` MUST be rejected at write time when the value is `closed` or `in_progress`. Transitions to `closed` MUST go through the `close` op; transitions to `in_progress` MUST go through the `claim` op. No raw `update_field` may bypass close or claim semantics.

### 5.A.5 Op filename hash length is chosen pre-write via shard probe; not stored in envelope
The op filename hash length MUST be chosen before write by probing the target shard directory under `.act/.lock`. The op file is written exactly once with the final-length hash in its filename. The filename hash is purely a filename disambiguator and MUST NOT be stored inside the envelope.

### 5.B.1 `op_hash` is sha256 of canonical JSON over a fixed-key object
`op_hash` is defined as `sha256(canonical_json({payload, hlc, node_id}))` over a fixed-key canonical JSON object containing exactly those three keys. The `||` notation in earlier prose is superseded; implementers MUST use the fixed-key object form.

### 5.B.2 Fold never calls HLC `receive`; `last_hlc` refreshes from `max(op.hlc)`
Fold MUST NOT call `receive`. HLC advancement happens only at op-write time. After fold, `last_hlc` MUST be refreshed from `max(op.hlc)` over the folded ops, ensuring fold remains pure and deterministic across machines.

### 5.B.3 Claim winner uses earliest-tuple; result is materialized as synthetic LWW write
The `claim` winner MUST be selected by the smallest `(wall, logical, op_hash)` tuple. The resulting `assignee` and `status=in_progress` writes MUST be materialized as a synthetic LWW write stamped with the winning claim's HLC. Any later LWW `assignee` op overrides the synthetic write per normal LWW rules.

### 5.B.4 `reopen` clears `closed_at` and `closed_reason` and resets their `last_hlc`
A `reopen` op MUST clear `closed_at` and `closed_reason` and reset their `last_hlc` to the reopen op's HLC. Subsequent `status=closed` ops may overwrite these fields again under the standard LWW rule that they are writable only by ops whose payload also carries `status=closed`.

### 5.B.5 Checkpoint reuse enumerates new issues via `git diff-tree`; missing subtrees drop
When `cp.tree_hash != current`, `new_issues` MUST be enumerated via `git diff-tree cp.tree_hash current -- .act/ops/`. Issues whose subtree is absent in `current` MUST be dropped from the new checkpoint and refolded as empty. Tombstone-only state is preserved through ops, not through cache retention.

### 5.C.1 `not found` is exit 3; exit 3 covers both env errors and resolution misses
`act show <id>` MUST return exit 3 when zero prefix matches resolve, per the pre-import id resolution pipeline. Exit 3 covers both environment errors (not in a git repo, missing `.act/`) and resolution misses. Exit 1 MUST NOT be used for not-found.

### 5.C.2 `update --status closed` is exit 2 unconditionally
`act update --status closed` MUST exit 2 unconditionally, regardless of the current state of the issue. `update` never accepts `closed` as a status value; the user MUST use `act close`. Idempotent already-closed handling applies only to `act close` itself.

### 5.C.3 `--claim` HLC drift check runs before pull-rebase
The HLC drift check (>5min from repo reference refuses with exit 1) MUST run before the implicit `git pull --rebase`, against the last known repo reference. This fails fast without performing network mutation.

### 5.C.4 Closed-parent warning under `--json` is embedded as `warnings: ["parent_closed"]`
Under `--json`, `act create --parent` to a closed issue MUST embed `"warnings": ["parent_closed"]` in the stdout JSON object and MUST suppress the human-readable stderr text. Without `--json`, the stderr warning text applies as documented.

### 5.C.5 `act dep add` dedup key is `(child, parent, type)`
The duplicate-edge idempotency key for `act dep add` is `(child, parent, type)`. Edges with different `--type` values between the same pair are distinct edges and MUST produce new ops. Only an exact `(child, parent, type)` match is idempotent (no op, exit 0).

### 5.D.1 `act_next` claim-loss backoff jitter is uniform `[0.75x, 1.25x]`, re-rolled per attempt
The `act_next` claim-loss backoff jitter MUST be uniform random in `[0.75x, 1.25x]` of the base delay (100ms, 400ms, 1.6s). The jitter MUST be re-rolled independently for each attempt.

### 5.D.2 `act_block` atomicity uses staged writes with single-commit semantics
`act_block` MUST write both the `set-status` and `dep-add` ops to a staging area, then `git add` and commit both in a single git commit. On any failure, both staged ops MUST be unstaged and deleted, and the underlying error class MUST be surfaced to the caller.

### 5.D.3 `stderr_tail` is last 4096 bytes UTF-8-trimmed; `truncated` reflects only the 64KB cap
`stderr_tail` is the last 4096 bytes of captured stderr, trimmed to the nearest valid UTF-8 boundary. `details.truncated` MUST be `true` if and only if the 64KB capture cap was hit, independent of the 4KB tail trim.

### 5.D.4 `compaction_locked` is a stdout warning under `--json`; error envelope reserved for non-zero exits
Under `--json`, `compaction_locked` MUST be emitted on stdout as `{"ok": true, "warning": "compaction_locked", "details": {...}}`. The `error` envelope is reserved for non-zero exits. The error-table row for `compaction_locked` belongs in a separate "warnings" subsection.

### 5.D.5 MCP test 7.5 budget reconciled with jitter math; exactly 3 claim attempts via injected clock
The base delay sum is `100ms + 400ms + 1.6s = 2.1s`; with per-attempt uniform jitter `[0.75x, 1.25x]` the achievable sleep range is `[1.575s, 2.625s]`. MCP E2E test 7.5 MUST inject a deterministic clock AND a deterministic jitter source so the test asserts exact, reproducible values: jitter sources MUST be seeded to produce `(1.0x, 1.0x, 1.0x)` (no jitter) and total elapsed sleep MUST equal `2.1s ± 50ms`. Per-attempt work time (fold + git ops) is excluded from the budget by using the injected clock to advance only on sleep; exactly 3 claim attempts MUST be observed.

## Coordination plane: Phase 2 config schema

Phase 2 (docs/coordination-plane-phase2-plan.md, ticket 1a) introduces a
small set of `act.*` git-config keys that live in `.act/.git/config` —
the nested .act repo's own git config file. These keys carry per-repo
orchestration knobs (timeouts, cache TTLs, drift thresholds) plus the
load-bearing `act.role` decision that closes v1 open-question #4.

### Config keys

`act remote enable` MUST write the following keys to `.act/.git/config`,
and `act remote disable` MUST unset all of them:

| Key | Value (default on enable) | Purpose |
|-----|---------------------------|---------|
| `act.role` | `orchestrator` | Role marker; see semantics below. |
| `receive.denyCurrentBranch` | `updateInstead` | Accept worker pushes into the checked-out branch. |
| `act.readCacheTTLSeconds` | `5` | Read-cache staleness budget for coordination-plane readers (ticket 2 series). |
| `act.bootstrapTimeoutSeconds` | `30` | Wall-time cap for the bootstrap protocol (ticket 7). |
| `act.fetchTimeoutSeconds` | `10` | Wall-time cap for an upstream `git fetch` (tickets 4 / 5). |
| `act.slowWriteThresholdMs` | `1000` | Per-write latency budget above which a coordination warning fires (ticket 8). |
| `act.upstreamDriftThresholdCommits` | `50` | Commit-count threshold for the orchestrator drift advisory. |
| `act.upstreamDriftThresholdSeconds` | `3600` | Wall-time-since-last-sync threshold for the drift advisory. |

### `act.role` semantics

`act.role` is the single mechanism for orchestrator-vs-worker
distinction. There is no filesystem-path heuristic.

- `act remote enable` sets `act.role=orchestrator` on the canonical-history holder.
- `act bootstrap-worker --from-remote` (Phase 2 ticket 7) sets `act.role=worker` on every dispatched worker.
- If the key is unset (legacy or hand-crafted repo), the parsed default is `worker` (safe — workers don't trigger upstream sync).
- The post-receive hook (ticket 6a) and the post-commit upstream-sync trigger (ticket 6b) read this key to decide whether to fire.

### Post-receive hook

`act remote enable` MUST install an executable file at
`.act/.git/hooks/post-receive`. Phase 2 ticket 6a fills in the body
(co-shipped with 1a in the same release window): the hook detaches a
background `act remote sync` via `nohup act remote sync >/dev/null 2>&1 &`
plus `exit 0`. The body deliberately retains a comment that names
ticket 6a so an agent reading the file knows who owns the body.

`act remote disable` MUST remove the file (not merely truncate it).

### Idempotency

Both verbs MUST be idempotent. `act remote disable` run on a
never-enabled repo MUST exit zero with no error; `act remote disable`
run twice in succession MUST exit zero both times. `act remote enable`
run twice in succession is also a no-op semantically (the second pass
re-writes the keys to the same defaults; the post-receive file is
re-installed from the same skeleton).

### Verification

`act remote enable` MUST run `act doctor` after the writes complete
and return non-zero if doctor reports any error-severity finding. This
catches enable runs that would leave a half-configured repo.

### Read-cache

Read-path commands (`act show`, `act ready`, `act log`, `act list`, `act search`) consult a TTL-bounded freshness gate before invoking `git fetch + git rebase` against the nested `.act/.git`. The gate exists so the canonical work loop's frequent reads do not pay the cost of a round-trip on every invocation — but it has explicit bypass paths for the cases (dispatcher fan-out, ad-hoc cache-bust) where staleness is unacceptable.

**TTL.** A 5-second TTL bounds the freshness window, measured against the mtime of `.act/.git/FETCH_HEAD`. Within the window, the read command reads on-disk state directly. Outside the window, the command calls `gitops.FetchAndRebase(branch)` first, then reads. A read on a cold cache (no `FETCH_HEAD` yet) is always a miss.

The TTL is currently a constant (5 seconds). Phase 2 ticket 1a introduces the `act.readCacheTTLSeconds` config key that will replace this constant with a per-repo override; the default and semantics remain unchanged.

**Bypass mechanisms.** Three paths force a fetch regardless of FETCH_HEAD age:

- `ACT_DISPATCH_MODE=1` env var. Set by the dispatcher when fanning out coordinated agents; ensures the worker's first read observes the dispatcher's latest push. The check is strict-literal: only the exact value `1` triggers the bypass.
- `--fresh` / `--no-cache` flag on `act ready`. Ad-hoc cache-bust for a single invocation; the two spellings are aliases with identical dispatch (both flags resolve to the same boolean at the cli boundary). The dual surface exists so agents reaching for the "no cache" idiom find a working flag without having to learn act's preferred spelling.

**Post-rebase invariant.** If a fetch-then-rebase advanced HEAD (i.e. the rebase added new ops to the nested `.act/.git`'s log), the cache layer invalidates the fold cache: `.act/fold-checkpoint.json` and `.act/index.db` are removed. The next read-path command's existing open+rebuild flow regenerates both from scratch over the new op set. The fold-checkpoint.json does not survive a rebase that adds ops. A no-op rebase (HEAD unchanged) leaves both files in place — otherwise every read on a quiet remote would defeat the cache.

**No-remote repos.** A repo with no `origin` configured on the nested `.act/.git` is a silent no-op for the cache layer: there is nothing to fetch from, so the freshness check is skipped and the read proceeds directly. This keeps single-machine / local-only flows unchanged.

**Failure modes.** A fetch error inside the cache layer (network unreachable, branch missing, rebase conflict) is non-fatal for the read-path command: the command falls through to read whatever state is currently on disk so a transient network failure does not break read-only commands. The write-path retry helpers (`PushWithRetry`, `FetchAndRebase` consumed by `--claim`) continue to surface their failures verbatim.

## `act remote sync` (Phase 2 ticket 6a)

`act remote sync` pushes the orchestrator's `.act/.git` to its
`origin-upstream` remote — the optional GitHub durability mirror
configured by `act remote add-upstream <url>` (Phase 2 ticket 6b
series). The subcommand is invoked two ways: directly by an agent at
the orchestrator, and detached in the background by the post-receive
hook (every worker push fires `nohup act remote sync &`).

### Behavior

- If `origin-upstream` is unset, exit 2 with envelope
  `upstream_not_configured`. The stderr line MUST be the literal
  `no origin-upstream configured; run 'act remote add-upstream <url>'`.
- If `origin-upstream` is set and its ref matches the orchestrator's
  local branch ref, exit 0 with no side effects (idempotent no-op).
- Otherwise run `git push origin-upstream <branch>`. On success, exit 0.
- On push failure (DNS, auth, unreachable bare path, …), exit 0 anyway
  and append one JSON-line entry to `.act/.sync-log`. The failure path
  is fail-soft so the post-receive hook never blocks a worker push on
  upstream connectivity.

### `.act/.sync-log` schema

`.act/.sync-log` is an append-only JSON-lines file. Each line is one
record. The fields are emitted in struct-declaration order so the
first JSON field on every line is `reason`:

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | Short slug. Current values: `unreachable`. |
| `timestamp` | RFC3339Nano | UTC time the failure was recorded. |
| `error` | string | Trimmed combined-output of the failing `git push`, capped at 4096 bytes (tail-preserving). |

The file is capped at 100 entries; older entries are dropped when an
append would push the count past the cap. The pruning shape matches
`.slow-writes` (ticket 8).

### Error-code entry

| Code | Exit | Where | Meaning |
|------|------|-------|---------|
| `upstream_not_configured` | 2 | `act remote sync` | No `origin-upstream` remote configured in `.act/.git/config`. Stderr line: `no origin-upstream configured; run 'act remote add-upstream <url>'`. |

### Orchestrator-write trigger (Phase 2 ticket 6b)

Every successful local commit by an `act` write subcommand on a
remote-configured project fires `act remote sync` in the background
when `act.role=orchestrator` is set in the nested .act/ repo's git
config. Workers (`act.role=worker` or unset) skip the trigger; their
publish path is the synchronous `git push` to the orchestrator (ticket
3a), and the orchestrator's post-receive hook (ticket 6a) then fans
the new ops out to `origin-upstream`. The orchestrator-write trigger
covers the symmetric leg: the orchestrator's own writes do not pass
through a post-receive hook, so the post-commit path is what
republishes them upstream.

Behavior:

- Role detection is config-key-based only: the trigger reads
  `act.role` from `.act/.git/config` via `config.ReadRole`. No
  filesystem-path heuristic is used (closes v1 OQ #4).
- The trigger uses `fork-exec` with `cmd.Start()` (no `cmd.Wait()`),
  `SysProcAttr.Setsid=true` for POSIX session detach, and
  stdin/stdout/stderr wired to `/dev/null`. The post-commit code path
  returns immediately; the spawned `act remote sync` runs
  independently and writes its own structured JSON-line records to
  `.act/.sync-log` (capped at 100 entries per the `act remote sync`
  schema above).
- `act.role=worker` or unset: the trigger is a no-op. Worker writes
  rely on the synchronous push + post-receive-hook chain rather than
  this trigger.
- A spawn failure (binary not on PATH, file-descriptor exhaustion, …)
  is silently swallowed: this is a publish-leg optimization, and the
  next orchestrator write will retry. The agent can also run `act
  remote sync` directly to catch up.

## `act remote add-upstream` (Phase 2 ticket 1b)

`act remote add-upstream <url>` configures the orchestrator's
`origin-upstream` remote to point at `<url>` and does an initial
`git push origin-upstream <branch>` so the mirror is seeded
immediately. Used to wire `act remote sync` (and the post-receive
hook's background sync) to a durable mirror — typically a GitHub repo
private to the agent run.

### Behavior

- Public-URL refusal: if the parsed host (case-insensitive) plus its
  first path component matches any entry in
  `internal/config/upstream_patterns.go` (`PublicHostPatterns`), the
  command exits 2 with envelope `upstream_public` and the stderr
  literal `refusing public upstream; pass --force-public to override`.
  No state is mutated on the refusal path.
- `--force-public` skips the public-URL refusal. The command otherwise
  succeeds as if the URL were private.
- On accepted URL: writes `remote.origin-upstream.url=<url>` and
  `remote.origin-upstream.fetch=+refs/heads/*:refs/remotes/origin-upstream/*`
  to `.act/.git/config` via `git config -f`, then runs
  `git --git-dir=.act/.git push origin-upstream <branch>` where
  `<branch>` is the branch named by `.act/.git/HEAD` (default `main`
  on detached HEAD).
- Initial push failure is NOT fail-soft (contrast with `act remote
  sync`): the command exits 3 with envelope `push_failed` carrying
  `details.url` and `details.branch`. The config writes are NOT rolled
  back — the user can re-run `add-upstream` (idempotent at the config
  layer) or run `act remote sync` once the URL is reachable.
- Idempotence: re-running with the same URL is safe; the `git config`
  writes overwrite to the same values and the push is a no-op when
  upstream already matches.

### Error-code entry

| Code | Exit | Where | Meaning |
|------|------|-------|---------|
| `upstream_public` | 2 | `act remote add-upstream` | URL matches a curated public-host pattern. Stderr line: `refusing public upstream; pass --force-public to override`. Pass `--force-public` to override. |

## `act bootstrap-worker --from-remote` (Phase 2 ticket 7)

`act bootstrap-worker --from-remote <url> <target-path>` clones the
orchestrator's `.act/.git` from a remote URL into the dispatched
worker's worktree. Phase 1.5's `act bootstrap-worker <target-path>`
(cwd-source) mode is preserved unchanged for sandboxed-no-network
workers; the two modes are mutually exclusive at the flag layer.

### Behavior

- Pre-flight: target parent MUST exist. Target `.act/` MUST be absent
  or empty unless `--force` is passed; otherwise exit 2 with envelope
  `target_not_empty`.
- Run `git clone --depth 1 <url> <target>/.act.bootstrap/` with a
  context-bound wall-clock deadline equal to
  `act.bootstrapTimeoutSeconds` (default 30, per the Phase 2 config
  schema). The flag `--timeout-seconds N` overrides this for tests
  and ad-hoc runs.
- On timeout: the clone subprocess is killed (SIGKILL via Go's
  `exec.CommandContext`), the staging dir is torn down, and the
  command exits 4 with envelope `bootstrap_timeout` carrying
  `details.timeout_seconds` and `details.url`.
- On a non-timeout clone failure (DNS, auth, unreachable URL, …) the
  staging dir is torn down and the command exits 3 with envelope
  `remote_unreachable` carrying `details.stderr_tail`.
- On successful clone: atomic-rename `<target>/.act.bootstrap/` →
  `<target>/.act/`, then write `act.role=worker` to
  `<target>/.act/.git/config` via `git config -f`. The worker role
  marker is the single mechanism the post-receive hook and the
  post-commit upstream-sync trigger use to decide whether to fire.
- Round-trip validate: run a `ready`-equivalent against the new
  target; tear it down on validation failure. Same shape as the
  cwd-source mode.
- Idempotent re-runs: a re-bootstrap into a non-empty target without
  `--force` is rejected with `target_not_empty`; with `--force`, the
  previous `.act/` is removed and the clone replays.

### Concurrency

Concurrent `act bootstrap-worker --from-remote` invocations into
disjoint target paths MUST NOT interfere: each clone uses a private
staging dir under its own target, and the role-config write touches
only the new target's nested `.git/config`. The §5 addenda
interference test asserts: "N parallel bootstraps to disjoint target
paths, each followed by `act ready`, all return the same state and
exit 0."

### Error-code entries

| Code | Exit | Where | Meaning |
|------|------|-------|---------|
| `bootstrap_timeout` | 4 | `act bootstrap-worker --from-remote` | Clone exceeded the wall-clock budget. Carries `details.timeout_seconds` (the value enforced) and `details.url`. |
| `target_not_empty` | 2 | `act bootstrap-worker` | Target `.act/` exists and is non-empty; `--force` is required to overwrite. Carries `details.target`. |

## Doctor reconciliation (Phase 2 ticket 9)

Phase 2 ticket 9 (act-aa4f19) extends `act doctor` with five new
reconciliation cases on top of the Phase 1 reconcile-lite surface
(docs/coordination-plane-design.md "Doctor reconciliation"). The cases
all read state local to the nested `.act/.git` repo — `act.role`,
`remote.origin.url`, `remote.origin-upstream.url`, and the loose-ref
SHAs of `refs/remotes/origin/<branch>` and
`refs/remotes/origin-upstream/<branch>`.

### Cases (a'), (c'), (f), (g), (h)

| Case | Check name | Trigger | Severity | Exit |
|------|------------|---------|----------|------|
| (a') | `remote-attached-orchestrator` | `act.role=orchestrator` AND `origin` configured AND no post-receive hook installed at `.act/.git/hooks/post-receive` | error | 1 |
| (c') | `worker-without-origin` | `act.role=worker` AND `origin` is unset | error | 1 |
| (f)  | `unpushed-commits` | `git rev-list --count origin/<branch>..HEAD` > 0 on the nested `.act/.git` | warn (error under `--strict`) | 0 |
| (g)  | `remote-reachable` | `git fetch origin --dry-run` fails inside `fetchDryRunTimeout` (5s) | error | 4 |
| (h)  | `upstream-drift` | `git rev-list --count origin-upstream/<branch>..origin/<branch>` > `act.upstreamDriftThresholdCommits` (default 50) | warn (error under `--strict`) | 0 |

Pinned stderr literals (these strings are user-visible and asserted by
the `TestDocClaim_DoctorCase_*` registry entries — drift in either the
spec or the implementation surfaces as a `go test` failure):

- Case (f): `local: <N> unpushed commits ahead of origin`
- Case (g): `remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity`
- Case (h): `upstream: origin-upstream is <N> commits behind origin; run 'act remote sync'`

Case (a') and case (c') findings carry remedy literals in their human
messages:

- Case (a'): the finding names the missing hook path and the recovery
  command (`act remote enable`).
- Case (c'): the finding includes the substring `act.role=worker but
  no origin configured` and names the recovery command
  (`act bootstrap-worker --from-remote`).

### `--no-fetch` flag

`act doctor --no-fetch` suppresses the inline `git fetch --dry-run`
probe used by cases (g) and (h). Per the §5 addendum: case (h)
detection requires a successful upstream fetch and cannot run against
stale cache, so `--no-fetch` suppresses case (h) emission entirely.
Case (g) under `--no-fetch` is downgraded to a warning (exit 0) so
operators on disconnected networks can still run doctor without the
reachability probe blocking the report.

### Remote-status JSON block

Doctor's `--json` output gains a top-level `remote_status` object
(always emitted; fields default to their zero values when not
applicable). The five fields:

```json
{
  "remote_status": {
    "remote_reachable": true,
    "local_unpushed_count": 0,
    "upstream_drift_commits": 0,
    "slow_writes_last_hour": 0,
    "fetch_failure_reason": ""
  }
}
```

Field semantics:

- `remote_reachable` — result of the case-(g) probe. True when the
  dry-run fetch succeeded OR when `origin` is not configured. False
  only when the probe ran and failed.
- `local_unpushed_count` — result of case-(f) `git rev-list --count
  origin/<branch>..HEAD`. Zero when `origin` is unconfigured.
- `upstream_drift_commits` — result of case-(h) `git rev-list --count
  origin-upstream/<branch>..origin/<branch>`. Zero when
  `origin-upstream` is unconfigured OR `--no-fetch` was passed.
- `slow_writes_last_hour` — count of entries in `.act/.slow-writes`
  whose `timestamp` parses as RFC3339 and falls within the last hour.
  Reads the schema pinned by ticket 3b (see "Slow-write observation").
- `fetch_failure_reason` — trimmed stderr of the failing case-(g)
  fetch probe. Empty unless the probe ran and failed. Bounded to 256
  bytes so a noisy upstream cannot blow up the JSON envelope.

## Stale-claim recovery (Mode B)

This section documents the **as-built** behavior. Surfaced by act-b5f8
following the act-334e abstractions review (85% confidence finding):
the orchestration-design doc claims Mode A is the degenerate case of
Mode B, but Mode A's "human notices a stuck claim and reclaims by
hand" mechanism collapses into "orchestrator must algorithmically
decide" in Mode B — and neither the spec nor the implementation
currently defines that algorithm.

**Current status: NOT IMPLEMENTED.** There is no stale-claim recovery
mechanism in `internal/claim/`, in the Phase 2 coordination-plane
plumbing (`internal/cli/cache.go`, `internal/cli/bootstrap_worker.go`,
`internal/cli/remote.go`), in doctor (`internal/cli/doctor_phase2.go`),
or in the fold layer (`internal/fold/apply.go`). The claim op has no
TTL field, no heartbeat schema, no lease, and no expiry. The
post-receive hook (Phase 2 ticket 6a) does not inspect claim age. The
`act.role=orchestrator` write path does not scan for stuck claims.
`act doctor` has no `stale-claim` check.

The four sub-questions the ticket calls out, and their concrete
answers under the as-built code:

### Detection

How is a stale claim identified? **Currently undefined.** The fold's
claim-apply rule (§5.B.3) is earliest-tuple-wins within the active
claim window, where the window is bounded on the right only by a
matching `close` op. An open claim has no timing predicate beyond
"there is no later close." A worker that wrote a `claim` op and then
disappeared leaves an `in_progress` issue indistinguishable from a
worker that is actively working. The op log carries no liveness
signal.

If a future implementation chose to add detection, the natural
candidates are: (a) wall-clock age of the winning claim op's HLC
exceeding a threshold (e.g. `act.claimStalenessSeconds`); (b) a
periodic `claim_heartbeat` op stamped by the live worker, with
absence of fresh heartbeats past `2 * heartbeat_interval` flagged as
stale; (c) orchestrator-side liveness inferred from the dispatcher's
own state (the orchestrator knows which workers it dispatched and can
treat a worker exit as implicit claim release). None of these exist
today.

### Grace period

How long before a claim is considered stale? **Currently undefined.**
No threshold lives in `internal/config/remote.go`'s key registry
(§"Coordination plane: Phase 2 config schema"). No default is encoded
anywhere in the codebase. Workers that die mid-claim leave the issue
in `in_progress` indefinitely until a human runs `act update
--assignee ""` to clear it or `act close` to terminate.

### Recovery action

What does the orchestrator do when it decides a claim is stale?
**Currently undefined; no orchestrator code path exists.** The Phase
2 orchestrator role (`act.role=orchestrator`) is wired only for
upstream-sync triggers (Phase 2 tickets 6a/6b — post-receive hook and
post-commit `act remote sync`). There is no orchestrator-side reaper
loop, no scheduled scan, no `act reclaim <id>` subcommand, and no
fold-time grace-period override.

If a future implementation chose to add recovery, the natural shape
under the existing op model is a **`release` op** (new op_type) that
clears the active claim window — symmetrically to how a `close` op
ends it. A second worker would then write a fresh `claim` op and win
under the standard earliest-tuple rule because the prior winning
claim is no longer in the active window. This stays within the
append-only, deterministic-fold contract (§6). Alternatives — mutating
the prior claim, deleting the op file, or special-casing assignee
swaps — all violate determinism. None of these implementations
exist today.

### Conflict resolution

What if the original claimant returns? **Currently undefined; the
question doesn't arise because nothing reclaims.** If a future
implementation added a `release` + re-`claim` sequence, the contract
follows from the existing fold semantics:

- A `release` op landed at HLC `R`. A new `claim` from worker B
  landed at HLC `R+1`. The original worker A's still-in-flight
  `claim` from before `R` is now suppressed (it predates the
  release, so it falls outside the active window opened by the new
  claim).
- If worker A's machine wakes up after a network partition and
  pushes a *new* claim op at HLC `R+2`, it loses by earliest-tuple
  to worker B's `R+1` claim. Worker A reads `claimed=false,
  winner=<B>` per the standard claim-loss envelope (§4), the same
  shape as a fresh-race loss.
- If worker A's machine has been off-network long enough that its
  HLC is far behind, the existing HLC plausibility check (§5.C.3)
  refuses the op at write time — worker A learns its clock is
  off and stops trying to claim. This is the same failure mode as
  any drift-skewed writer.

The recovery story therefore composes cleanly with existing
primitives **if** the release op is added. Until then, a returning
claimant's behavior is governed only by the absence of any reclaim
event — i.e., the issue is still theirs because nobody changed it.

### Implications for Mode B orchestrators

A Mode B orchestrator implementing dispatch today cannot rely on act
for stale-claim recovery. The orchestrator MUST either:

1. **Track liveness out-of-band.** Maintain orchestrator-side state
   (process IDs, dispatch timestamps, container lifecycle events)
   and treat the act claim as advisory. When the orchestrator
   decides a worker is dead, it clears the assignee with `act
   update <id> --assignee ""` and re-dispatches. This is what the
   `/orchestrate` reference flow does implicitly today: each
   dispatch pass is a fresh orchestrator process whose worker
   inventory is rebuilt from the dispatch log, not from `act
   ready` output.

2. **Tolerate stuck claims as a degenerate Mode A case.** Surface
   long-`in_progress` issues for human review (e.g. via `act list
   --status in_progress --older-than 24h`, a flag that does not
   yet exist). This collapses Mode B's "algorithmic decision" back
   into Mode A's "human notices" — explicitly accepting the
   asymmetry the act-334e review identified.

Neither approach is endorsed by act itself; both are escape hatches
the orchestrator builds on top of act's primitives.

### Forward path

Closing this gap is a separate work item. The two-mechanism design
sketch (HLC-age detection + `release` op for recovery) is the
likeliest shape because it stays inside the append-only / deterministic
fold contract and reuses existing config-key plumbing. A spec section
on the release op would live near §4 (Atomic claim protocol); a
config key (e.g. `act.claimStalenessSeconds`) would live in the Phase
2 config schema table; a `stale-claim` doctor check would extend the
Phase 2 reconciliation table. None of these are filed as tickets at
the time of writing.

This section will be promoted from "currently undefined" to a
specification as and when the implementation lands. Until then, the
explicit answer for any Mode B orchestrator asking "how does act
handle stale claims?" is: **it does not; the orchestrator owns this
concern.**
