# act Implementation Spec

Version: 0.1.0 spec.

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
| `closed_at` | Yes | Derived from close-op HLC; records when the issue was closed. |
| `closed_reason` | Yes | W3 sets it; doctor uses it to distinguish abandoned vs completed. |
| `machine` | Yes | A ticket that can only be done on one machine is not ready on the others; without this the constraint lives in prose in the title and every consumer has to parse it. Empty means "runs anywhere", which is the default and what every op written before the field folds to. |
| labels/tags | **Dropped** | W1-W3 never need free-form tagging; `type` + `parent` cover grouping. Reintroduce only if a concrete workflow demands it. |
| estimate / time-tracking | **Dropped** | Anti-goal in brief. |
| comments | **Dropped** | Description edits + child issues cover the discussion need. |
| watchers | **Dropped** | Single-user-with-agents; no notification fan-out. |

### Issue schema (folded state)

```jsonc
{
  "id":           "act-a1b2",                  // string, on-disk short id, "act-" + N hex chars (4 <= N <= 16)
  "title":        "string, 1..200 chars, required",
  "description":  "string, 0..1048576 bytes (1 MiB), default \"\"",
  "status":       "open | in_progress | closed",              // default "open"; "blocked" is a DERIVED display state computed from open blocked-by dep edges — not stored, not directly settable, never produced by the fold
  "priority":     0,                           // int 0..3 inclusive, default 2 (lower = more urgent)
  "type":         "task | bug | epic | chore", // default "task"
  "parent":       "act-... | null",            // default null
  "deps": [
    { "parent": "act-...", "edge_type": "blocks | relates | supersedes" }
  ],
  "blocked_by": ["act-...", "..."],             // bare ids from deps where edge_type=blocks (derived)
  "blocks":     ["act-...", "..."],             // bare ids of issues that list this issue as a blocks parent (reverse scan)
  "assignee":     "string | null",             // default null; free-form (human handle or agent role)
  "machine":      "string | null",             // default null = runs anywhere; a machine label (printable ASCII, no spaces, <=64 bytes). Compared case-insensitively against the running machine's own label; `act ready`/`act next` exclude a non-matching issue, `act list`/`act show` never do
  "acceptance_criteria": [
    { "text": "string, 1..500 bytes", "done": false }
  ],
  "created_at":   "RFC3339 UTC, from create-op HLC wall",
  "closed_at":    "RFC3339 UTC | null",
  "closed_reason":"string | null"
}
```

Validation rules:
- `id` matches `^act-[0-9a-f]{4,16}$`. IDs on disk are always the short form, capped at 16 hex chars (the collision-extension procedure in §ID model never grows past this cap). The 40-hex (actually 64-hex) sha256 digest is an internal intermediate value used to derive the short id; it is never written as an `id` or `issue_id` field.
- `priority` outside 0..3 is a hard error at write time.
- `parent` and every `deps[].parent` MUST resolve to an existing (non-tombstoned) issue at fold time; doctor's `dangling-deps` check enforces this on the whole repo.
- `acceptance_criteria` indices are stable across the issue's lifetime; `remove_accept` shifts later indices down (see op semantics below).
- A close op against an issue with unmet criteria succeeds only if `closed_reason` is non-empty; doctor flags otherwise.

Length caps. Every length limit on a written field is counted in **bytes** — Go's `len()` of the UTF-8 string — not characters, so a string of multi-byte characters reaches the cap before it reaches that many characters. Each is enforced at write time by the op payload validators in `internal/op/payloads.go`, and each is one named constant there.

| Field | Cap | Constant |
|---|---|---|
| `title` (`create`, `update_field`) | 256 bytes | `MaxTitleLen` |
| `description` (`create`, `update_field`; `act create`/`update --description`, `--description-file`, the merged result of `--description-append`/`--description-append-file`, MCP `act_create`/`act_update`, `act import`) | 1048576 bytes (1 MiB) | `MaxDescriptionLen` |
| each acceptance criterion (`create.accept[i]`, `add_accept.criterion`, `set_accept.criteria[i]`) | 500 bytes | `MaxAcceptCriterionLen` |
| `reason` on `close`, `reopen`, `unclaim` (`act close`/`finish`/`reopen --reason`) | 500 bytes | `MaxReasonLen` |
| external dep `ref` (`add_external_dep`, `remove_external_dep`) | 256 bytes | `MaxExternalRefLen` |
| `machine` label | 64 bytes | `MaxMachineLabelLen` |

An over-cap description exits 2 with error `bad_flag` on every `act create`/`act update` path — inline, file and append, the same code and key as an over-cap title. The acceptance-criterion cap is the reason cap by design (`MaxAcceptCriterionLen` is defined as `MaxReasonLen`). `act delete --reason` has its own, larger 4096-byte cap. Caps bind writes and imports only: folding existing ops never re-checks them, so an op written before a cap existed stays readable.

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
create       | update_field | add_dep       | remove_dep | add_external_dep |
remove_external_dep         | add_accept    | remove_accept | set_accept    |
claim        | unclaim      | close         | reopen     | redact           |
import       | migrate      | tombstone
```

These seventeen are the types a writer may emit. `redact` is legacy and
parse-only: it is accepted on read but has no writer and no apply path, so the
fold skips it. An op whose `op_type` is not in this list is skipped by the
fold, not a hard failure (see §Op-fold algorithm).

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
{ "criterion": "string, 1..500 bytes" }     // appended; index = current len

// remove_accept — exactly one of index/text is required
{ "index": 0 } | { "text": "exact match string" }

// set_accept — REPLACES the full acceptance list (LWW-gated on the accept
// field). This is the replace primitive `act update --accept` emits, so
// repeated edits set rather than union. Empty list clears all criteria.
{ "criteria": ["string, 1..500 bytes", ...] }

// claim — atomic assignee + status; sugar over two update_fields, but recorded as one op
{ "assignee": "string" }

// close
{ "reason": "string?, <=500 bytes" }       // sets status=closed, closed_at=hlc.wall, closed_reason

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
  .gitignore                       # host repo: `act init` adds the line `.act/`
  .act/                            # a git repo of its own (the nested repo); the host repo ignores all of it
    .git/                          # the nested repo's git dir; .git/info/exclude holds the patterns listed under "excluded"

    # tracked: committed in the nested repo; travels with the tracker
    config.json                    # see schema below
    ops/
      act-a1b2/
        2026-04/
          2026-04-29T14-23-01.000Z-7f3a9c1d-create.json
          2026-04-29T14-24-15.123Z-9c2bf014-claim.json
          2026-04-29T15-02-44.901Z-3e1f88a2-update_field.json
        2026-05/
          ...
    hooks/
      close.sample                 # written and committed by `act init`; "Hooks contract" names the files that are run
    snapshots/
      act-a1b2.json                # canonical folded state at last compaction (see "Compaction")
    imports/
      2026-04-29T14-23-01.000Z.json   # { "source": "...", "mapping": { ... } }

    # excluded: machine-local working files; never staged, matched by .git/info/exclude
    .write.lock                    # flock target that serializes act writers in this checkout
    .slow-writes                   # slow-write log (see "Slow-write observation")
    .pending-pushes                # commits not pushed yet (see "Offline mode")
    .sync-log                      # `act remote sync` log
    .compact.lock                  # flock target for compaction
    index.db                       # SQLite, derived from ops/
    index.db-wal
    index.db-shm
    index.db-journal
    index.db.malformed-*           # corrupt index kept by `act doctor --fix-index`
    .doctor-rebuild.db*            # doctor's scratch index
    fold-checkpoint.json           # { "tree_hash": "<git-tree-of-.act/ops>", "computed_at": "...", "schema_version": 1 }
    fold-checkpoint.json.tmp
    .bootstrap-meta.json           # stamp `act bootstrap-worker` leaves in a worker's copy
    .sync-log.tmp
    .tmp-*                         # temp file of an atomic rewrite
    config-*.json.tmp              # temp file of the config.json rewrite

    # untracked: op files act moved aside; never staged and not excluded, so `git -C .act status` shows them
    .failed-ops/                   # ops whose stage or commit failed
    .collisions/                   # local op files that differed from origin's copy
```

The tree lists every path act writes at the top of `.act/`, in three groups. The host repo sees none of them: `act init` adds the line `.act/` to the host `.gitignore`, and the `gitignore-effective` doctor check reports a host repo that does not ignore it. Inside the nested repo, only the paths under "tracked" are committed. The paths under "excluded" are kept out of the nested repo by `.act/.git/info/exclude`, not by a `.gitignore`: `act init` writes those patterns there, and the `nested-exclude` doctor check repairs a store that lacks them. `.failed-ops/` and `.collisions/` are neither committed nor excluded. Nothing writes a `.act/.lock`.

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

```sql
CREATE TABLE issues (
    id            TEXT PRIMARY KEY,
    title         TEXT,
    description   TEXT,
    status        TEXT,
    priority      INTEGER,
    type          TEXT,
    parent        TEXT,
    assignee      TEXT,
    created_at    TEXT,
    claimed_at    TEXT,
    closed_at     TEXT,
    closed_reason TEXT,
    machine       TEXT,
    tombstoned    INTEGER DEFAULT 0
);
CREATE TABLE issue_accept (
    issue_id  TEXT NOT NULL,
    idx       INTEGER NOT NULL,
    criterion TEXT,
    PRIMARY KEY(issue_id, idx)
);
CREATE TABLE issue_deps (
    issue_id  TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    edge_type TEXT NOT NULL,
    PRIMARY KEY(issue_id, parent_id, edge_type)
);
CREATE TABLE issue_external_deps (
    issue_id TEXT NOT NULL,
    ref      TEXT NOT NULL,
    PRIMARY KEY(issue_id, ref)
);
CREATE TABLE issue_meta (
    issue_id       TEXT PRIMARY KEY,
    schema_version INTEGER
);
CREATE TABLE index_state (      -- one key today: 'ops_build_key'
    key   TEXT PRIMARY KEY,
    value TEXT
);
CREATE TABLE index_issue_sig (  -- per issue: what its ops subtree looked like when its rows were written
    issue_id TEXT PRIMARY KEY,
    sig      TEXT NOT NULL
);
CREATE INDEX idx_status   ON issues(status);
CREATE INDEX idx_priority ON issues(priority);
CREATE INDEX idx_parent   ON issues(parent);
CREATE VIRTUAL TABLE fts USING fts5(
    issue_id UNINDEXED,
    title,
    description,
    tokenize = 'unicode61'
);
```

That block is the whole schema: seven tables, three indices (`idx_status`, `idx_priority`, `idx_parent`, all on `issues`) and the `fts` full-text table. `index_state.ops_build_key` records what `.act/ops/` looked like when these rows were built — a signature over every op file's path, size and mtime, qualified by the writing binary's version — so a reader can tell a current index from a stale one without refolding. It lives in `index.db` rather than beside it so the key and the rows it describes commit or roll back together; a key in a separate file could outlive a failed rebuild and vouch for rows that were never written.

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
    # Envelope checks. Any failure aborts the whole fold with an error
    # naming the op file and the field; there is no named error code.
    require(op.op_version == 1)
    require(op.schema_version == 1)             # equality, not a ceiling
    require(is_semver(op.writer_version))       # shape only, no upper bound
    # An unrecognised op_type is NOT an error here: the op parses, sorts,
    # and is skipped at step (5). Only an empty op_type is rejected.
    op.op_hash = sha256(canonical_json(op.payload || op.hlc || op.node_id))
    parsed.append(op)

  # (3) Sort: HLC ascending, op_hash tiebreaker
  parsed.sort(key = (op) -> (op.hlc.wall, op.hlc.logical, op.op_hash))

  # (4) Initialize per-issue state lazily on first op for that issue_id
  issues = {}

  # (5) Apply left-to-right with per-op semantics (see 3)
  for op in parsed:
    state = issues.get(op.issue_id) or empty_issue(op.issue_id)
    issues[op.issue_id] = state
    if not known_op_type(op.op_type): continue    # skipped, never applied
    issues[op.issue_id] = apply(state, op)

  # (6) Emit
  return {issues, index: build_index(issues)}
```

**Unknown op types are skipped, not fatal.** An op with an unrecognised op_type is skipped by the fold, not a hard failure:
the reader parses it with every other envelope check still in force (a
malformed envelope still aborts the fold), then passes over it without applying
it, and every other op folds as usual. A tracker that holds ops written by a
newer `act` therefore stays readable by an older binary — `act list`, `act
show` and `act ready` keep answering, minus the effect of the ops they do not
understand. The same skip covers the legacy parse-only `redact` type.

**Version fields on read.** An op whose schema_version is not the one this binary writes aborts the fold with an
error naming the op file and `schema_version`. `act list` and `act ready` fold
the whole op log and exit 1; `act show` folds one issue, so it exits 3 on the
issue carrying the op and still answers for the others. The same check applies
to `op_version`.
The check is equality, so it rejects an older value as well as a newer one.
The fold does not compare `writer_version` at all — it only requires a
well-formed semver — so a newer writer_version does not stop the fold, and
that op is applied like any other. Writer-version skew is reported by
`act version --check-repo`, which exits 4 with `version_skew`.

`empty_issue` has all scalar fields nil, sets `deps={}`, `acceptance={}`,
`tombstoned=false`, and a per-field `last_hlc` map used for LWW.

### 3. Per-field conflict resolution

The `apply(state, op)` function dispatches per `op_type`. Default merge is
last-writer-wins (LWW) by `(hlc.wall, hlc.logical, op_hash)`. The exceptions
below are normative.

| Field / op | Rule |
|---|---|
| `title`, `description`, `priority`, `type`, `assignee`, `parent` | LWW per field. Each carries its own `last_hlc`; an op with a strictly-greater HLC tuple wins; equal tuples cannot occur (op_hash differs). |
| `status` | Constrained transition table. Stored values are `open`, `in_progress`, and `closed` only — `blocked` is NOT a stored status value. `closed` is terminal: an op transitioning out of `closed` is ignored unless its `op_type` is `reopen` (an explicit op kind). Within `{open, in_progress}` LWW applies. `claim` ops set `(assignee, status=in_progress)` atomically (see 4). **Derived display state:** an issue displays as "blocked" when `status` is `open` and it has at least one open blocked-by dep edge (added via `act dep add --blocked-by`); this derived state is never stored in the fold and never produced by the fold as a stored status. The `blocked_requires_dep` guard (added in act-36b6d3) enforces that `act update --status blocked` exits 2 unless a backing blocked-by dep edge exists; when one does, the command writes no op (no `update_field status` op, `ops_written: 0`, `already_blocked: true`) because the issue's display state already follows from its deps. |
| `acceptance_criteria` | Grow-shrink set keyed by criterion hash, plus an LWW replace. `add_accept` is idempotent set-add; `remove_accept` requires a referenced criterion hash and is set-remove. Adds and removes are commutative; on tie at the same hash, the op with greater HLC wins (remove-wins for equal HLC is impossible because op_hashes differ). `set_accept` REPLACES the whole list via an LWW gate on the `accept` field and clears the removed-set; later-stamped add/remove ops resume grow-shrink from the new baseline. |
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
   git commit --no-verify -m "act-op: (<short-id>) claim"

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

These flags are accepted by the commands that write an op — `create`, `update`, `close`, `dep add`, `reopen`, `delete` — except `--verify`, which only `update`, `reopen` and `delete` take. `act init` takes none of them. Each command's Synopsis line names the ones it accepts.

- `--no-commit` — write the op file and stage it; do not run `git commit`. Useful for batching agents.
- `--push` — after the op-commit, run `git push` to the configured remote. Implies a successful local commit.
- `--isolated` — local-only mode. Skips remote fetch (relevant for `--claim`'s pull-rebase step) and refuses `--push`. Mutually exclusive with `--push`.
- `--verify` — run host git hooks during the op-commit. Default is `--no-verify` (op commits touch only `.act/ops/**`). `create`, `close` and `dep add` do not define this flag: passing it to them is exit 2.
- `--offline` — commit locally and skip the push; see "Offline mode" below.
- `--branch NAME` — commit the op on branch `NAME` of the nested `.act/` repo instead of the branch it is on. The branch is created if it does not exist and is left checked out.

Precedence rules:
1. `--no-commit` + `--push` is a usage error (exit 2): cannot push what wasn't committed.
2. `--isolated` + `--push` is a usage error (exit 2): isolated forbids network.
3. `--verify` + `--no-commit` is silently a no-op (no commit happens, hooks irrelevant).
4. `--claim` (on `update`) implicitly fetches unless `--isolated` is set.

Auto-publish on write (Phase 2, act-65a7d5). When the nested `.act/` repo has `origin` configured, every successful auto-commit on a write subcommand (`create`, `update`, `close`, `dep add`, `reopen`, `delete`) is followed by a synchronous `git push` via the retry helper documented in §"push retry". `--push` becomes redundant in that case; setting it is harmless. No-origin repos skip the publish step silently — the op log stays local-only without ceremony.

**Publish failure is a deferral, not an error (act-89a595).** Once the auto-commit lands, the op is durable, so the publish leg can no longer fail the command. When the push does not reach origin — retry exhaustion, an unreachable remote, a rejecting receive — the command **exits 0**, appends the commit to `.act/.pending-pushes`, and writes an unmissable `act: WARNING:` block to stderr saying the op was committed but not pushed. `act close --json` additionally sets `push_deferred: true` (and `push_deferred_reason`) on its result, so JSON consumers can tell "peers can't see it yet" from "it didn't land". The deferred commit publishes on the next non-offline write, or on `act remote sync`; a bare push on the nested repo covers every local commit reachable from HEAD, so nothing is lost by waiting. The `push_exhausted` envelope (exit 4) that this path used to emit is **retired** — it reported failure on a write that had already landed, and callers applying the ordinary `rc != 0 ⇒ the write didn't happen` rule retried and duplicated the op.

**Untracked op-file collisions are resolved by act, not by the operator (act-650378).** The publish path's fetch-and-rebase detaches HEAD onto `origin/<branch>`, and git refuses to detach when the checkout would overwrite an untracked working-tree file — `error: could not detach HEAD`, listing paths under `ops/`. That state arises routinely: an op can exist on disk while origin already tracks its path (concurrent writers in a shared checkout, a state import). act resolves it itself and retries the rebase once, rather than emitting git's "Please move or remove them" — an instruction to delete files inside the tracker's own state directory. The policy is conservative and reported on stderr:

- Only paths under `ops/` are touched, and only ones git reports as untracked. A colliding path outside that lane aborts the whole resolution and the original rebase error surfaces unchanged.
- A local file byte-identical to origin's copy is **deleted** — the checkout is about to write the same bytes.
- A local file that **differs** is **moved**, never deleted, to `.act/.collisions/<timestamp>/<original-path>`.

The fail-soft boundary is exactly the commit: every failure *before* the auto-commit (op write, staging, hook, commit itself) still exits non-zero with its own envelope, because nothing landed in those cases. The local commit is never rolled back on push failure: the op file is on disk and is recoverable via the harvest path. The `remote_unreachable` envelope is a `bootstrap-worker` clone-failure outcome, not a write-path one.

`act harvest` is **not** in the auto-publish list. Harvest is the orchestrator-side fan-in primitive; the orchestrator's nested `.act/.git` typically has no `origin` remote (it IS the canonical history workers push to), so auto-pushing after a harvest commit would be a silent no-op in the common production shape. In Phase 2 remote-attached mode the worker already pushed its ops to the orchestrator before harvest ran (harvest short-circuits with a skip for that shape). In Phase 1.5 sandboxed mode, publishing a harvest fold upward to an upstream remote is an orchestrator-level concern handled by `act remote sync`, not by harvest itself.

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
- `4` — not one meaning. Three commands exit 4, each for its own condition: `act version --check-repo` when some op file carries a `writer_version` newer than this binary's (`version_skew`); `act doctor` when `origin` is configured but unreachable (the `remote-reachable` finding); and `act state import --from-remote` when the clone runs past its time budget (`bootstrap_timeout`). No other command exits 4 — reads and writes apply an op from a newer writer like any other op.

All non-zero exits emit `{"error": {"code": <int>, "kind": "<slug>", "message": "..."}}` on stdout when `--json` is set; on stderr in human mode.

### Pre-import id resolution

Every command accepting an `<id>` argument resolves the input through this pipeline, in order, and uses the first hit:

1. **Import maps**: scan `.act/imports/*.json` (sorted by filename, descending) for the input as a key in any `mapping` object. If found, replace with the mapped post-import id and continue.
2. **Exact full id**: if the input matches a known full id, use it.
3. **Prefix match**: treat the input as a prefix and find issues whose full id starts with it. Zero matches → exit 3 ("not found"). One match → use it. Multiple matches → exit 2 with `{"error": "id_ambiguous", "message": "...", "details": {"prefix": "act-a1b", "candidates": ["act-a1b2", "act-a1b9", ...]}}`. Nothing emits the older `ambiguous_id` code.

Resolution happens before any op is written, so a write command never partially applies due to a bad id.

---

### `act init`

**Synopsis:** `act init [--force] [--force-new] [--contributing] [--commit-host] [--json]`

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

**Synopsis:** `act create <title> [-p|--priority N] [--parent ID] [--blocked-by ID]... [--blocks ID]... [--accept "criteria"]... [--type T] [--machine LABEL] [--description "text"] [--description-file PATH] [--json] [--no-commit] [--push] [--isolated] [--offline] [--branch NAME]`

**Flags:**
- `-p, --priority N` (int 0..3, default 2). 0 is highest.
- `--parent ID` (string, optional). Resolved via id-resolution pipeline. Hierarchy only — NOT a dep edge; use `--blocked-by`/`--blocks` for blocking.
- `--blocked-by ID` (string, repeatable). The new issue is blocked BY each ID; writes one blocks-edge per ID alongside the create op in a single atomic commit. The new issue stays out of `act ready` until that ID closes.
- `--blocks ID` (string, repeatable). The new issue blocks each existing ID — the inverse direction; each existing ID's deps grow so it waits on the new issue. An ID appearing in both `--blocked-by` and `--blocks` is a 2-cycle → exit 2. Duplicates within a flag fold to one edge.
- `--accept "criteria"` (string, repeatable). Each invocation appends one acceptance-criterion string.
- `--type T` (enum task|bug|epic|chore, default task).
- `--machine LABEL` (string, default ""). Pins the new issue to one machine: `act ready`/`act next` on any other machine exclude it, while `act list`/`act show` show it everywhere. The default, empty, means "runs anywhere". Filing time is when the constraint is known, which is why the flag lives here and not only on `act update`. Label rules: printable ASCII, no spaces or control characters, ≤64 bytes; a violation is exit 2 at the flag boundary, never a written op.
- `--json` (bool, default false for humans, true under MCP).

**Behavior:** Builds a `create` op payload, hashes `(payload || nonce)` to derive the new issue id `act-<N hex>` where `4 <= N <= 16` (shortest non-colliding prefix per §ID model), runs the `create` hook, and only once the hook passes writes the op file at `.act/ops/<id>/<yyyy-mm>/<iso>-<hash6>-create.json` and op-commits it. The `create` hook runs before the create op file is written (§Hooks contract), so a hook that refuses the create leaves no op file behind. Under `--no-commit` the op file is written, no hook runs and nothing is committed.

**JSON output:**
```json
{"ok": true, "id": "act-a1b2c3d4e5f60718", "prefix": "act-a1b2",
 "op_id": "7f3a...", "committed": true, "pushed": false}
```

**Exit codes:** 0, including a create whose push was deferred; 1 if the `create` hook refuses the create (`hook_failed`), or the op cannot be written, committed or indexed (including `write_lock_timeout` and `stale_git_lock`); 2 on a usage error — an unknown flag, a missing or empty title, a value outside a flag's range or cap, a conflicting flag pair, or an ambiguous `--parent`/`--blocked-by`/`--blocks` prefix (`id_ambiguous`); 3 outside a git working tree (`not_in_git`), in a repo with no `.act/` (`act_not_initialized`, or `tracker_not_checked_out` when a configured tracker remote holds this repo's tracker), when a `--parent`/`--blocked-by`/`--blocks` id matches no issue (`issue_not_found`), or when `--description-file` names a file that does not exist (`file_not_found`). `act create` never exits 4: an op from a newer writer already in the tracker does not stop the write.

**Edge cases:** `--parent` to a closed/redacted issue is allowed with a stderr warning; empty title or title >256 bytes → exit 2; hash collision with an existing id retries the nonce up to 8 times before exit 1.

---

### `act list`

**Synopsis:** `act list [--status X] [--all] [--assignee Y] [--type T] [--json] [--limit N] [--sort field] [--no-fetch]`

**Flags:**
- `--status X` (csv string, e.g. `open,in_progress`). Default: all non-closed — the working set. `--status closed` reaches finished work.
- `--all` (bool). Lists every status, closed included. Mutually exclusive with `--status` (the pair is exit 2).
- `--assignee Y` (string, exact match). Default: any.
- `--type T` (enum). Default: any.
- `--json` (bool).
- `--limit N` (int, default 200).
- `--sort field` (enum priority|created_at|closed_at|id, optionally suffixed `:asc`/`:desc`). Default sort order: priority asc, then created_at desc. Tie-breaker: id asc.
- `--no-fetch` (bool). Skip the read-path fetch and rebase and answer from the `.act/` state on disk; `--json` output then carries `refresh.reason: "no_fetch"`. `ACT_NO_FETCH=1` does the same.

**Ordering:** closed issues sort after every non-closed issue. This grouping is applied ahead of `--sort` and is not overridable — a listing that mixes statuses must not bury live work under finished work. `--sort` orders the rows within each group.

**Behavior:** Reads from `.act/index.db`. The index is refolded from `.act/ops/` only when the op tree has changed since the rows were written; an unchanged op tree is answered from the index without a refold. The comparison is re-derived from the op tree on every read rather than from a marker a writer maintains, so an op appended by another process, or one removed by a rolled-back write, forces the refold and no read can answer from an index the op log no longer supports. The refold is scoped to what moved: a read taken after a write refolds only the issues whose ops subtree changed, and drops an issue whose subtree disappeared, so the cost of the read after a write tracks the change rather than the size of the op log.

**JSON output:**
```json
{"ok": true, "count": 12, "issues": [
  {"id":"act-a1b2c3d4e5f60718","prefix":"act-a1b2","title":"...",
   "status":"open","priority":1,"type":"task","assignee":"agent-1",
   "parent":null,"created_at":"2026-04-29T14:23:01Z",
   "claimed_at":"2026-04-29T15:01:44.318Z"}
]}
```

**Exit codes:** 0, including an empty result; 1 when the index cannot be opened, brought up to date with the op log, or queried (`index_open_failed`, `index_rebuild_failed`, `index_query_failed`) — an op file the fold rejects, such as one with an unknown `op_version`, lands here; 2 on a usage error — an unknown flag, a positional argument, `--all` with `--status`, an unknown `--status` value or `--sort` field; 3 outside a git working tree (`not_in_git`), or when a configured tracker remote holds a tracker this checkout has not cloned (`tracker_not_checked_out`). In a git repo with no `.act/` and no tracker remote, `act list` prints the no-state line and exits 0. `act list` never exits 4: an op with a newer `writer_version` is applied like any other, and a refresh from `origin` that fails is a stderr warning, not a failure.

**Edge cases:** empty result returns `count: 0` and exit 0; unknown sort field or `--all` combined with `--status` → exit 2. `--limit 0` means "no limit" (every match). `claimed_at` is present on rows that have been claimed and omitted on the rest, so claim age is readable from a listing without one `act show` per in-progress id.

---

### `act show <id>`

**Synopsis:** `act show <id> [--json] [--include-ops] [--commit-marker] [--full] [--no-fetch]`

**Flags:**
- `--json` (bool).
- `--include-ops` (bool, default false). Inlines the full op stream alongside the folded snapshot.
- `--commit-marker` (bool). Print only the commit-message trailer for the issue — one line, `Act-Id: <id>`, using the issue's short id — and exit 0. A tombstoned issue has no marker and exits 3.
- `--full` (bool). Human output only: print `description` and `closed_reason` whole. Without it each is cut at 5 lines or 400 characters, whichever comes first, and ends with `… (truncated; see --json for full text)`. `--json` output is never cut.
- `--no-fetch` (bool). Skip the read-path fetch and rebase and answer from the `.act/` state on disk. `ACT_NO_FETCH=1` does the same.

**Behavior:** Resolves id (errors with exit 2 on prefix ambiguity), folds the issue, prints snapshot.

**JSON output:**
```json
{"ok": true, "issue": {
  "id":"act-a1b2c3d4e5f60718","title":"...","description":"...",
  "status":"open","priority":1,"type":"task","parent":null,
  "deps":[{"parent":"act-9c2b...","edge_type":"blocks"}],
  "blocked_by":["act-9c2b..."],
  "blocks":["act-ff3a..."],
  "assignee":"agent-1","acceptance_criteria":["..."],
  "created_at":"...","closed_at":null,"closed_reason":null,
  "ops_count": 4
}, "ops": [/* present iff --include-ops */]}
```

**Exit codes:** 0, including a tombstoned issue; 2 on a usage error — an unknown flag, a missing `<id>`, a second positional argument, or an ambiguous prefix (`id_ambiguous`); 3 outside a git working tree (`not_in_git`), when a configured tracker remote holds a tracker this checkout has not cloned (`tracker_not_checked_out`), when the id matches no issue (`issue_not_found`), when the issue's op stream cannot be read or folded (`fold_failed` — an op with an unknown `op_version` lands here), or when `--commit-marker` is asked of a tombstoned issue. In a git repo with no `.act/` and no tracker remote, `act show` prints the no-state line and exits 0. `act show` never exits 4: an op with a newer `writer_version` is applied like any other.

---

### `act update <id>`

**Synopsis:** `act update <id> [--title T] [--status X] [--priority N] [--type T] [--parent ID] [--assignee Y] [--machine LABEL] [--description T] [--description-append T] [--description-append-file PATH] [--description-file PATH] [--accept "..."] [--accept-add "..."] [--accept-rm N] [--dep-rm ID] [--ext-rm REF] [--claim] [--unclaim] [--force] [--json] [--wait] [--wait-timeout D] [--no-commit] [--push] [--isolated] [--offline] [--branch NAME] [--verify]`

`act update` reaches every one of the seven LWW-per-field updatable fields the fold table in §3 names — `title`, `description`, `priority`, `type`, `assignee`, `parent`, `machine`. This is normative: a field the fold merges per-field MUST have a write path on both the CLI and MCP surfaces, or the storage layer supports a state no caller can reach. Title, type and parent were added in act-3e21b8 to close exactly that gap.

**Flags:**
- `--title T` (string). REPLACES the title; ≤256 bytes, matching `act create`. Non-empty is required — unlike `--assignee`/`--description`, `--title ""` is exit 2 rather than a clear, because every `act list` / `act ready` row renders the title and nothing else of the body. Retitling is deliberately cheap: a title that has gone stale misleads every dispatcher before they reach the correcting text in the description.
- `--status X` (enum open|blocked|in_progress|closed). `closed` requires `act close` — exit 2. `in_progress` requires `--claim` — exit 2. `blocked` requires a backing blocked-by dep edge (added via `act dep add --blocked-by`); if none exists, exits 2 with `blocked_requires_dep`. Note: `blocked` status is derived from open `blocks` dep edges, so with the dep edge present `--status blocked` writes no op: it exits 0 with `ops_written: 0`, `committed: false`, `already_blocked: true` (human: `Unchanged <id>: already blocked by its dep edge (no ops written)`).
- `--priority N` (int 0..3).
- `--type T` (enum task|bug|epic|chore). Any other value → exit 2 with `bad_flag`; the closed set matches `act create --type`.
- `--parent ID` (string). Sets the hierarchy parent — NOT a dep edge; use `act dep add` for blocking. Resolved through the id-resolution pipeline, so a prefix works (unknown → exit 3, ambiguous → exit 2). `--parent ""` detaches the issue from its parent. An issue naming itself, or a value whose existing parent chain already reaches this issue, → exit 2 with `cycle_detected`: `act doctor`'s `cycle` check walks the *blocks* subgraph only, so a parent cycle has no downstream detector and is refused at the write path instead.
- `--assignee Y` (string; empty string clears).
- `--machine LABEL` (string). Pins the issue to one machine, or un-pins it: `--machine ""` returns it to the default, "runs anywhere". Same label rules as `act create --machine`. Compared case-insensitively against the running machine's own label (`act machine`).
- `--description T` (string). REPLACES the body.
- `--description-file <path|->` (string). Reads the replacement body from a file, or from stdin for `-`. Mutually exclusive with `--description`.
- `--description-append T` (string). APPENDS to the existing body, separated by one blank line, resolving the current body server-side — the note-append path, so annotating an issue is one command rather than a read-modify-write of the whole body. Mutually exclusive with both replace flags.
- `--description-append-file <path|->` (string). The file twin of `--description-append`, for a note too long or too quote-hostile for one argv token; `-` reads stdin. Mutually exclusive with `--description`, `--description-file` and `--description-append`. Note the append resolves against the folded snapshot and is not atomic: two concurrent appends to one issue are last-write-wins on the description field, exactly as two concurrent `--description` writes are.
- `--accept "..."` (repeatable). REPLACES the acceptance list with exactly the supplied criteria — it does not append. Emits one `set_accept` op so repeated edits set rather than union with prior criteria. Supplying `--accept` with no criteria clears the list.
- `--accept-add "..."` (repeatable; each appends one criterion via `add_accept` — the additive sibling of `--accept`).
- `--accept-rm N` (repeatable; removes the criterion at zero-based index `N` against the current list via `remove_accept`. Out-of-range is a no-op. Replace an individual criterion with `--accept-rm N --accept-add "new text"`).
- `--dep-rm ID` (repeatable). Removes a dep edge; resolves id.
- `--claim` (bool). Atomic claim protocol: write `claim` op, `git pull --rebase` (skipped under `--isolated`), refold, report win/loss. On win, optionally push if `--push`.
- `--wait` (bool, only with `--claim`). On loss, poll until the issue becomes claimable again or status becomes terminal.
- `--wait-timeout D` (Go duration such as `90s`, default `30s`). Bound on `--wait`. A bare number such as `60` is not a duration and is exit 2.
- `--json` (bool).

Each non-`--claim` field flag generates one op (so `--priority 0 --assignee "me"` writes two ops in one commit).

**JSON output (claim win):**
```json
{"ok": true, "claimed": true, "id":"act-a1b2c3d4e5f60718",
 "winner":"7f3a91c2","ops_written":["claim","set-priority"]}
```
**JSON output (claim loss):**
```json
{"ok": false, "claimed": false, "id":"act-a1b2c3d4e5f60718",
 "winner":"9c2bdead","error":"claim_lost","reason":"lost-race"}
```
Exit codes for `--claim`: `0` win, `5` loss (envelope `claim_lost`, per the universal exit-code table in §error-envelope), `1` other logical error, `2` usage. Other update modes follow universal exit codes.

**Edge cases:** `--claim` with no remote configured warns on stderr and proceeds local-only; HLC drift >5min from repo reference refuses the op (exit 1); `--dep-rm` of a non-existent edge → exit 1 (logical, not usage); `--wait` without `--claim` → exit 2.

**Retitling and commit correlation.** A retitle does not disturb commit-marker correlation or `act doctor`'s `orphan-close` check. Both key on the issue **id** — the `Act-Id: act-XXXX` commit-body trailer and the id column in the index — never on the title, which is not an identity key. `act show --json`'s `commits` array and `act doctor --check orphan-close` therefore return the same results before and after `act update --title`.

---

### `act close <id>`

**Synopsis:** `act close <id> [--reason TEXT] [--force] [--no-code] [--no-doctor] [--json] [--no-commit] [--push] [--isolated] [--offline] [--branch NAME]`

**Flags:**
- `--reason TEXT` (string, default ""). Stored as `closed_reason`.
- `--json` (bool).

**Behavior:** Builds a `close` op carrying `closed_at` and `closed_reason`; runs the `close` hook, and only once the hook passes writes the op file. The `close` hook runs before the close op file is written (§Hooks contract), so a refused close leaves no op file behind. It then commits the op file on its own in the nested `.act/` repo. The close commit subject is `act-op: (<short-id>) close`, where `<short-id>` is the issue id, cut to `act-` plus six hex characters when it is longer — the parenthesized marker is what `act doctor`'s `orphan-close` check greps for. The close writes one op file and nothing else: there is no `closed_by_tree` value, and the index has no `closed_by_tree` column.

**JSON output:**
```json
{"ok": true, "id":"act-a1b2c3d4e5f60718", "closed_at":"...",
 "closed_reason":"shipped", "committed":true, "pushed":false}
```

**Exit codes:** 0, including a close of an issue that is already closed (no op is written; `--json` answers `already_closed: true`) and a close whose push was deferred; 1 if the `close` hook refuses the close (`hook_failed`), or the op cannot be written, committed or indexed (including `write_lock_timeout`, `stale_git_lock`, and an index the op log cannot rebuild); 2 on a usage error — an unknown flag, a missing `<id>`, a `--reason` over the cap, a conflicting flag pair, an ambiguous prefix (`id_ambiguous`), or an open external dep without `--force` (`blocked_by_external_dep`); 3 outside a git working tree (`not_in_git`), in a repo with no `.act/` (`act_not_initialized`, or `tracker_not_checked_out` when a configured tracker remote holds this repo's tracker), or when the id matches no issue (`issue_not_found`). `act close` never exits 4: an op from a newer writer already in the tracker does not stop the close.

**Edge cases:** closing an issue with open children is allowed and surfaced by `doctor orphan-close`; reason >500 bytes → exit 2 (see Length caps).

---

### `act dep add <child> <parent>`

**Synopsis:** `act dep add <child> <parent> [--type T] [--blocked-by ID] [--blocks ID] [--external REF] [--json] [--no-commit] [--push] [--isolated] [--offline] [--branch NAME]`

**Flags:**
- `--type T` (enum blocks|relates|supersedes, default blocks).
- `--json` (bool).

**Behavior:** Resolves both ids, refuses if it would create a cycle in the `blocks` subgraph (cycle detection runs over the full folded graph). Writes one `add_dep` op on the child issue.

**Direction (edge_type=blocks):** the `child` is blocked BY the `parent` — the child stays out of `act ready` until the parent closes. `child` is the dependent, `parent` is the blocker. The raw `child`/`parent` fields read backwards as subject-verb-object, so the output carries a `summary` field rendering the edge in its natural direction.

**JSON output:**
```json
{"ok": true, "child":"act-a1b2...", "parent":"act-9c2b...",
 "type":"blocks", "summary":"act-a1b2... is blocked by act-9c2b...",
 "committed":true}
```

**Exit codes:** 0; 1 on cycle (`{"error":{"kind":"cycle","path":[...]}}`); 2 on bad flags; 3 on missing id.

**Edge cases:** self-edge → exit 2; duplicate edge is idempotent (no op, exit 0); `relates`/`supersedes` are not cycle-checked.

---

### `act ready`

**Synopsis:** `act ready [--under <id>] [--json] [--limit N] [--all-machines] [--mine] [--as NODE] [--fresh] [--no-cache] [--no-fetch]`

**Flags:**
- `--under <id>` (string, optional). Restrict output to descendants (via `parent` edges) of this id.
- `--json` (bool).
- `--limit N` (int, default 50). `--limit 0` means "no limit" (every ready issue), the same meaning it has on `act list`.
- `--all-machines` (bool). Include issues pinned to another machine.
- `--no-fetch` (bool). Skip the read-path fetch and rebase and answer from the `.act/` state on disk; `--json` output then carries `refresh.reason: "no_fetch"`. `ACT_NO_FETCH=1` does the same.

**Algorithm:** Fold all issues; an issue is **ready** iff `status == open` AND no incoming `blocks` dep points at it from an open/in_progress issue AND it is not pinned to a different machine. Sort by priority asc, created_at desc, id asc.

**Machine filtering runs BEFORE `--limit`**, so `total` means "machine-eligible ready rows before the cap". An aggregator that treats `count >= limit` as "total unknown" therefore never sees a cap applied to rows it could not use anyway.

**The machine filter only runs when this machine has an EXPLICIT label** — `$ACT_MACHINE` or `$XDG_CONFIG_HOME/act/machine`. A label merely inferred from the hostname is reported but never filters: it is the one layer that can change underneath a fleet (an OS rename, a lost config file), and a label matching nothing would exclude every pinned issue on every machine while suppressing its own alarm, since a store whose ready count fell to zero is one nothing launches into. So act fails open on an unnamed machine and says so on stderr.

**JSON output:**
```json
{"ready": [{"id":"...","short_id":"...","title":"...","priority":0,"status":"open","created_at":"...","machine":"laptop"}],
 "count": 3, "total": 3, "truncated": false,
 "machine": {"label":"mini","source":"config","filtered":true,"pinned_elsewhere":7}}
```

`count` is how many rows were returned; `total` is how many were ready before `--limit` was applied; `truncated` says outright whether the cap dropped rows. A capped ready set also prints a WARNING to **stderr** naming how many issues were hidden — in both `--json` and human mode, since the JSON consumer's human sees nothing else when stdout is piped. Callers must test `truncated` rather than infer truncation from `count == limit`, which is wrong exactly when the ready count equals the limit.

The top-level `machine` object is **always emitted**, so its ABSENCE is how a consumer detects an act that predates machine affinity. `label` and `source` are this machine's identity; `filtered` says whether pinned-elsewhere rows were actually excluded; `pinned_elsewhere` counts rows needing another machine **either way** — dropped when `filtered`, still present in `ready` when not. `filtered: false` with `source: "hostname"` means the machine was never named; with any other source it means `--all-machines`. The per-row `machine` key appears on pinned rows in both modes.

**Exit codes:** 0, including an empty ready set; 1 when the index cannot be opened, brought up to date with the op log, or queried (`index_open_failed`, `index_rebuild_failed`, `index_query_failed`) — an op file the fold rejects, such as one with an unknown `op_version`, lands here; 2 on a usage error — an unknown flag, a positional argument, `--as` without `--mine`, `--no-fetch` with `--fresh`/`--no-cache`, or an ambiguous `--under` prefix (`id_ambiguous`); 3 outside a git working tree (`not_in_git`), when a configured tracker remote holds a tracker this checkout has not cloned (`tracker_not_checked_out`), when `--under` matches no issue (`issue_not_found`), or when `--mine` cannot read `.act/config.json`. In a git repo with no `.act/` and no tracker remote, `act ready` prints the no-state line and exits 0. `act ready` never exits 4: an op with a newer `writer_version` is applied like any other.

---

### `act next`

**Synopsis:** `act next [--under <id>] [--limit N] [--peek] [--all-machines] [--isolated] [--json]`

`--all-machines` requires `--peek`: a bare `act next --all-machines` exits 2. `act next` claims, and claiming an issue pinned to another machine sets it `in_progress`, which removes it from the ready set on **every** machine — so the flag is allowed only on the read-only path.

The composed `ready` → claim → `show` step of the work loop: it picks the top
claimable issue from the ready frontier, **claims it**, and shows it.

**Flags:**
- `--under <id>` (string, optional). Restrict the ready frontier to descendants of this id.
- `--limit N` (int, default 50). How many ready candidates to consider.
- `--peek` (bool). **Read-only survey.** Show the issue `act next` would claim and claim nothing.
- `--isolated` (bool). Offline mode for the claim: commit but no network ops.
- `--json` (bool).

**`--peek` is the read-only path (act-4ffd57).** It runs the same ready → pick →
show flow and stops before the claim: no op is written, and the issue it names is
left with `status == open` and no assignee. Use it whenever you are **surveying**
what is available rather than taking it — screening a pool, sweeping several repos,
deciding where to send a worker. Peeking is advisory, not a reservation: the issue
it names can be claimed by anyone between your peek and your claim, which is exactly
why a claim is a separate step.

The flag exists because `next` reads like a query. Sessions surveying queues across
several repos claimed work they never meant to take, and a spurious claim is not
cosmetic — the claim is the mechanism that stops two sessions working one ticket, so
for as long as it stands a session that legitimately wants that work is told it is
taken. A surveying session that dies before releasing leaves the claim standing with
nobody to release it, and the ticket reads as in-progress to every later reader.

**A bare `act next` still claims, and always will.** The claim is `next`'s contract:
orchestrator drains depend on it, and a `next` that stopped claiming would hand one
ticket to several workers at once. `--peek` is purely additive. The claiming form
leads its human output with `CLAIMED <id>` and the `act update <id> --unclaim`
release command, so a caller who reaches for the wrong one sees it on line one.

**JSON output** — claiming form:
```json
{"claimed": true, "issue": {...}, "commit_marker": "Act-Id: act-XXXX"}
```
**JSON output** — `--peek`:
```json
{"claimed": false, "peek": true, "would_claim": {...}}
```
The peek shape deliberately does not reuse the `issue` key: a consumer reading
`issue` out of a peek response would be one field name away from believing it holds
a claim. An empty or fully-contended frontier returns `{"claimed": false,
"candidates": [...]}` in both forms, exit 0.

**Exit codes:** 0 (including "nothing claimable"); 2 bad flags; 3 not in a git repo
or missing `.act/`; otherwise the underlying claim/show envelope's code.

---

### `act search <query>`

**Synopsis:** `act search <query> [--in title|desc|all] [--status X] [--limit N] [--no-fetch] [--json]`

**Flags:**
- `--in` (enum, default `all`). Restrict FTS5 column scope.
- `--status X` (csv).
- `--limit N` (int, default 50).
- `--no-fetch` (bool). Skip the read-path fetch and rebase and answer from the `.act/` state on disk; `--json` output then carries `refresh.reason: "no_fetch"`. `ACT_NO_FETCH=1` does the same.
- `--json` (bool).

**Behavior:** SQLite FTS5 query; index rebuilt-on-demand if stale.

**JSON output:**
```json
{"ok": true, "count": 5, "results":[
  {"id":"...","prefix":"...","title":"...","snippet":"...","rank":-3.41}
]}
```

**Exit codes:** 0, including no matches; 2 on a usage error — an unknown flag, a missing or empty query, a second positional argument (quote a multi-word query), an `--in` value other than `title|desc|all`, a negative `--limit` — and on any error SQLite returns for the query itself; 3 outside a git working tree (`not_in_git`), when a configured tracker remote holds a tracker this checkout has not cloned (`tracker_not_checked_out`), or when the index cannot be opened or brought up to date with the op log (`index_open_failed`, `index_rebuild_failed` — the failures `act list` reports as exit 1 are exit 3 here). The query is quoted token by token before it reaches FTS5, so FTS5 operators and unbalanced quotes are searched for literally, not rejected as syntax. In a git repo with no `.act/` and no tracker remote, `act search` prints the no-state line and exits 0. `act search` never exits 4: an op with a newer `writer_version` is indexed like any other.

---

### `act log <id>`

**Synopsis:** `act log [<id>] [--since D] [--by-issue ID] [--type T[,T...]] [--summary] [--no-fetch] [--json]`

**Flags:**
- `--since D` (duration). Only ops newer than `D` ago. Takes Go durations (`30m`, `24h`) and a whole-token day form (`7d`); a mixed form such as `1d12h` is rejected.
- `--by-issue ID` (string). Only ops of this issue, by full id or unique prefix — the same scope as the positional `<id>`. Passing both with different values exits 2.
- `--type T[,T...]` (string). Only ops of these op types, comma-separated (for example `create,close`). An unknown type exits 2.
- `--summary` (bool). Human output only. The human format is one line per op either way — timestamp, op type, 8-character hash, `[issue=<short id>]` — and `--summary` adds a short note of what the op did (the title of a create, the reason of a close, the field of an update) and drops the issue tag when the log is scoped to one issue. `--json` output is the same with or without it.
- `--no-fetch` (bool). Skip the read-path fetch and rebase and answer from the `.act/` state on disk. `ACT_NO_FETCH=1` does the same.
- `--json` (bool).

**Behavior:** Emits the chronological op stream (HLC-sorted). The `<id>` is optional: with an `<id>` or `--by-issue` the stream is that one issue's; with neither, it spans every issue, and at least one of `--since`, `--type` or `--summary` must then be given — a bare `act log` exits 2. `--since` and `--type` narrow the stream and combine with each other and with the issue scope. Includes `op_id`, `op_type`, `payload`, `hlc`, `node_id`, `writer_version`, and the file path.

**JSON output:**
```json
{"ok": true, "id":"act-a1b2c3d4e5f60718", "ops":[
  {"op_id":"7f3a...","op_type":"create","hlc":"...","node_id":"...",
   "writer_version":"0.1.0","payload":{...},"path":".act/ops/act-a1b2.../..."}
]}
```

**Exit codes:** 0; 2 on a usage error — an unknown flag, no `<id>` and no filter flag, a second positional argument, an unparseable `--since`, an unknown `--type`, a positional id that disagrees with `--by-issue`, or an ambiguous prefix (`id_ambiguous`); 3 outside a git working tree (`not_in_git`), when a configured tracker remote holds a tracker this checkout has not cloned (`tracker_not_checked_out`), when the id matches no issue (`issue_not_found`), or when an op file in scope cannot be read or parsed (`ops_read_failed` — an op with an unknown `op_version` lands here). In a git repo with no `.act/` and no tracker remote, `act log` prints the no-state line and exits 0. `act log` never exits 4: an op with a newer `writer_version` is printed like any other.

---

### `act doctor`

**Synopsis:** `act doctor [--check NAME] [--fix] [--fix-index] [--strict] [--no-fetch] [--json]`

**Flags:**
- `--check NAME` (string, a single value). Run only the named check. The flag is not repeatable: passed twice, the last value is the one that runs. Default: run all. The names accepted are the rows of the table below; any other name exits 2.
- `--fix` (bool). Auto-remediate where safe (per check, below). Drives `index-divergence`, `index-schema` and `nested-exclude` only.
- `--fix-index` (bool). Auto-remediate the `index-malformed` check by rebuilding `.act/index.db` from `.act/ops/`. Separate flag from `--fix` so the malformed-image recovery (which moves the broken file aside) is opt-in independent of the routine divergence/schema fixes.
- `--strict` (bool). Promote every warn-severity finding to error severity, so a run whose only findings are warnings exits 1 instead of 0.
- `--no-fetch` (bool). Skip the `git fetch --dry-run` probe of `origin` that `phase2-reconciliation` runs; see "`--no-fetch` flag" under "Doctor reconciliation". Not the read-path flag of the same name: `ACT_NO_FETCH=1` does not set it.
- `--json` (bool).

**Checks and `--fix` behavior:**

| Check | Algorithm | `--fix` / `--fix-index` effect |
|---|---|---|
| `stale-git-lock` | Look for `index.lock` and `HEAD.lock` in the nested repo's git dir (`.act/.git/`). Either one, left behind by an interrupted commit, makes every act write fail until it is removed. One error-severity finding per lock file present; its message names the file and the recovery commands, and in human mode is also written bare to stderr. | Read-only, under `--fix` too: doctor never removes the lock, because it cannot tell a stale lock from one a running git process holds. A finding exits 1. |
| `orphan-close` | Find issues with `closed_at` set but no commit message containing `(act-<prefix>)` AND no diff in `.act/ops/<id>/` at close time. | Read-only; surfaces finding. |
| `orphan-ops` | Op files referencing an `issue_id` that has no `create` op. | Read-only. |
| `fold-error` | The op log cannot be folded: an op file fails to parse or validate. Not a pass of its own in a default run — it is reported as one error-severity finding whenever a selected check needs the fold (`orphan-close`, `orphan-ops`, `dangling-deps`, `cycle`, `status-vs-oplog`) and the fold fails, and `--check fold-error` folds and reports only this finding. In human mode the finding's message is also written bare to stderr. When the fold fails, only `stale-git-lock`, `time-travel`, `unknown-op-version` and `gitignore-effective` still run (those that were selected); the checks that need the fold, the three index checks, `nested-exclude` and `phase2-reconciliation` do not. | Read-only. With the fold failed, `--fix` and `--fix-index` apply nothing: no check that honours them runs. |
| `dangling-deps` | Dep edges pointing at unknown ids (post-resolution). | Read-only. |
| `time-travel` | Adjacent ops with HLC going backward more than the 5-minute drift bound. | Read-only. |
| `cycle` | Cycle in the `blocks` subgraph. | Read-only. |
| `unknown-op-version` | Any op file whose `op_version` is greater than the one this binary writes (1). It reads only that field from the raw JSON; `writer_version` is not examined, so an op from a newer writer with a known `op_version` is not a finding (that skew is `act version --check-repo`'s to report). | Read-only: `--fix` changes nothing and the op file is left in place. One error-severity finding per offending op file, so the run exits 1. |
| `status-vs-oplog` | Compare each issue's status across three views: the index (`.act/index.db`), the op files on disk under `.act/ops/`, and the op log committed at `HEAD` of the nested repo. An error-severity finding when the index holds a status the op files on disk do not give (a stale index), and when an op file committed at `HEAD` is missing from `ops/` and the status changes with it (an op retracted with no record). A warn-severity finding when op files on disk are not committed yet and they change the status: the status act reports is not durable yet. Op-file differences that leave the status unchanged are not findings, and neither is an issue the index has no row for. In human mode each finding's message is also written bare to stderr. | Read-only: `--fix` does nothing for this check. Error findings exit 1; the warn finding exits 0 unless `--strict`. |
| `gitignore-effective` | Ask the host repo whether `.act/` is ignored (`git check-ignore .act/`). If it is not, the tracker's files can be committed into the host repo's history: one error-severity finding whose message gives the remedy (add `.act/` to `.gitignore`, then `git rm -r --cached .act/`). A probe that cannot run is an error finding too. | Read-only: `--fix` does not edit `.gitignore`. A finding exits 1. |
| `phase2-reconciliation` | One name for the five remote cases described under "Doctor reconciliation" below, run in a single pass: `remote-attached-orchestrator`, `worker-without-origin`, `unpushed-commits`, `remote-reachable`, `upstream-drift`. Findings carry those five names, never `phase2-reconciliation`, and `--check` takes only the umbrella name — the five case names are not accepted. This is the check that fills `remote_status` in the JSON output. | Read-only: `--fix` changes nothing. `remote-attached-orchestrator` and `worker-without-origin` are error findings (exit 1); `unpushed-commits` and `upstream-drift` are warnings (exit 0 unless `--strict`); `remote-reachable` exits 4 when `origin` is configured and the fetch probe fails, and is a warning under `--no-fetch`. |
| `index-malformed` | Try `index.Open` and `PRAGMA integrity_check` on `.act/index.db`. The malformed signal is the canonical SQLite phrasing — `database disk image is malformed` (SQLITE_CORRUPT) or `file is not a database` (SQLITE_NOTADB). | With `--fix-index`: renames the broken file to `.act/index.db.malformed-<unix-nano>`, opens a fresh SQLite at the canonical path, and runs `index.Rebuild(paths.Ops)` to repopulate from the op log. Without `--fix-index`: emits an error-severity finding whose message ends with the literal `rebuild with 'act doctor --fix-index'`. |
| `index-divergence` | Recompute index into a tmp SQLite from ops/snapshots; row-by-row diff against `.act/index.db` (recomputed = oracle). | Replaces `.act/index.db` with the recomputed db. When `.act/index.db` is malformed, this check defers to `index-malformed` (returns no finding) to avoid stderr-noise duplication. |
| `index-schema` | Compare index schema version to expected. | Drops and rebuilds `.act/index.db`. When `.act/index.db` is malformed, this check defers to `index-malformed` to avoid stderr-noise duplication. |
| `nested-exclude` | Read the nested repo's `.act/.git/info/exclude` and look for each of these lines, which between them cover every act working file at the top of `.act/`: `/.write.lock`, `/.slow-writes`, `/.pending-pushes`, `/.sync-log`, `/.compact.lock`, `/index.db`, `/index.db-wal`, `/index.db-shm`, `/index.db-journal`, `/index.db.malformed-*`, `/.doctor-rebuild.db*`, `/fold-checkpoint.json`, `/fold-checkpoint.json.tmp`, `/.bootstrap-meta.json`, `/.sync-log.tmp`, `/.tmp-*`, `/config-*.json.tmp`. These are machine-local files act never stages; unlisted, they show as untracked in `git -C .act status` and a blanket stage there would commit them. Any missing lines are reported as one warn-severity finding that names them. `act init` writes these lines, and so does `act state import` in every mode, so only a store made by an older act, or one cloned by hand with `git clone`, lacks them. | Appends the missing lines; existing content is kept and a second `--fix` changes nothing. |

#### `index-malformed` recovery semantics (act-f2f93a)

`.act/index.db` is a derived cache only — the `.act/ops/` op log is the source of truth. When the SQLite file goes malformed (typical cause: an external crash mid-write or a filesystem-level corruption event), every write command that touches the index — `act create`, `act close`, `act update`, `act dep add`, `act ready` — fails with a wrapped `database disk image is malformed` error before reaching ops.

`act doctor --fix-index` is the standalone recovery path:

1. The check inspects `.act/index.db` via `index.Open` + `index.IntegrityCheck` (which runs `PRAGMA integrity_check`). The two known SQLite malformed signals — `database disk image is malformed` and `file is not a database` — are matched by the `index.IsMalformed(err)` helper.
2. On a positive detection without `--fix-index`: a single error-severity finding is emitted; its message contains the literal `rebuild with 'act doctor --fix-index'` (and the cmd/act dispatcher mirrors this to stderr alongside the bracketed human-text output).
3. On a positive detection with `--fix-index`: the broken `.act/index.db` is renamed to `.act/index.db.malformed-<unix-nano>` (preserved for post-mortem — no destructive delete), any stray `*-wal` / `*-shm` / `*-journal` siblings are removed, and a fresh `index.Open` + `index.Rebuild(paths.Ops)` repopulates the canonical path. The finding's severity is downgraded to warn and its message names the backup file and the count of issues folded.
4. On a healthy index, `--fix-index` is a no-op: no backup, no rewrite, exit 0.

The fix does NOT destroy the broken copy — operators (and the agent's later passes) keep the renamed file alongside the rebuilt one until they manually decide it's no longer needed.

**JSON output:**
```json
{"findings":[
  {"check":"nested-exclude","severity":"warn","message":"the nested .act repo does not ignore act's working files (/.slow-writes): ..."}
], "count":1,
 "remote_status":{"remote_reachable":true,"local_unpushed_count":0,"upstream_drift_commits":0,"slow_writes_last_hour":0,"fetch_failure_reason":""}}
```

The object has exactly three top-level keys: `findings`, `count` and `remote_status`. There is no `ok` key, no per-check `checks` list and no pass/fail summary — a check that found nothing leaves no trace in the output. `findings` is `null` on a clean run; each finding has `check`, `severity` (`warn` or `error`) and `message`. `count` is the number of findings. `remote_status` is described under "Remote-status JSON block".

**Exit codes:** 0 when no finding has error severity (warnings alone exit 0 unless `--strict` promotes them); 1 if any error finding remains (and `--fix` did not remediate) — an `unknown-op-version` finding exits 1 like any other error finding, not 4; 2 on bad flags, including a `--check` name that is not a check; 3 when the working directory is not inside a git working tree (`not_in_git`), or when a configured tracker remote holds a tracker this checkout has not cloned (`tracker_not_checked_out`); 4 when `origin` is configured but unreachable (the `remote-reachable` error finding), which takes precedence over 1. In a git repo with no `.act/` and no tracker remote, `act doctor` is a read command like any other: it prints the no-state line and exits 0.

---

### `act mcp`

**Synopsis:** `act mcp [--read-only] [--workdir DIR]`

**Flags:**
- `--read-only` (bool). Server-level: refuses any write tool call regardless of per-call `read_only`.
- `--workdir DIR` (string). Chdir before serving; required when launched outside the repo.

**Behavior:** Stdio MCP server, no network. Tool surface specified below.

**Protocol revisions.** The server is dual-era. A legacy client opens with `initialize` and is served under protocol `2024-11-05`, unchanged. A client on the stateless `2026-07-28` revision skips the handshake and sends `io.modelcontextprotocol/protocolVersion` and `io.modelcontextprotocol/clientCapabilities` in each request's `params._meta`; such requests are answered without initialize, carry `resultType` and `_meta."io.modelcontextprotocol/serverInfo"` on every result, and `tools/list` adds the cache hint `ttlMs` + `cacheScope`. `server/discover` advertises the supported modern version. A `_meta` version other than `2026-07-28` returns `-32022` (UnsupportedProtocolVersion) listing the supported versions.

**Host-repo resolution.** Each `tools/call` resolves the host repo independently. If the client names a workspace out-of-band on the call — Codex sends `_meta."x-codex-turn-metadata".workspaces` (a map keyed by absolute workspace path), since it launches the server with cwd = the plugin install dir and advertises no MCP `roots` capability — that workspace is authoritative: the server resolves the host repo from it rather than from its own process cwd. Absent any client workspace hint (Claude Code, which launches the server in the project dir; or the direct CLI), the server falls back to cwd-based resolution. This keeps repo-relative tools operating on the user's project regardless of where the plugin host launches the long-lived server process.

**Exit codes:** 0 on clean shutdown (the client closes stdin); 1 on a transport error while serving; 2 on an unknown flag, or a `--workdir` the server cannot change into. A missing git repo or a missing `.act/` is not a startup failure: the server starts in any directory, and a tool call that needs tracker state answers with an error result (`no_repo`) while the server keeps running. The `act mcp` process never exits 3 or 4.

---

### `act machine`

**Synopsis:** `act machine [--set <label>] [--json]`

Prints **this machine's label** — the value an issue's `machine` pin is compared against — and where it was resolved from. Needs no repo and no `.act/`: the label is a property of the machine, not of a tracker, which is why it lives in one per-machine file rather than in each store's `config.json`.

**Resolution order:**
1. `$ACT_MACHINE`, if set and non-empty.
2. `$XDG_CONFIG_HOME/act/machine` (default `~/.config/act/machine`), first line, trimmed.
3. The short hostname, lower-cased — `os.Hostname()` up to the first `.`.

Layers 1 and 2 are **explicit**; only they cause `act ready`/`act next` to filter. Layer 3 is reported but never filters (see `act ready`). act ships no mapping from hostname to label: the mechanism is act's, the machine names are the operator's.

**Flags:**
- `--set <label>` (string). Write the label to the per-machine file and print the result. `--set ""` removes the file, restoring the hostname fallback.
- `--json` (bool). `{"machine":"mini","source":"hostname","config_path":"..."}` — `config_path` is where `--set` writes, whatever the active source is.

**Exit codes:** 0; 2 bad flag or invalid label.

---

### `act version`

**Synopsis:** `act version [--check-repo] [--json]`

**Flags:**
- `--check-repo` (bool). Walk every op file under the `.act/ops/` of the repo the working directory is in — the repo root is found from any subdirectory, so the flag need not be run from the root — to find `max(writer_version)`; compare to `self.writer_version`. Exits 4 if `self < max`. Outside a git working tree, or in a repo with no `.act/`, it exits 3 (see the exit codes below).
- `--json` (bool).

**JSON output:**
```json
{"ok": true, "binary_version":"0.1.0", "writer_version":"0.1.0",
 "schema_version":1, "repo_max_writer_version":"0.1.0"}
```

**Exit codes:** 0; 2 on an unknown flag. With `--check-repo` only: 3 outside a git working tree (`not_in_git`), when the repo has no `.act/` (`no_repo`), or when `.act/` cannot be read — the check resolves the repo root from the working directory like the other read commands, so it gives the same answer from any subdirectory of the repo; 4 on skew, meaning some op file carries a `writer_version` newer than this binary's (`version_skew`). `act version --check-repo` is the only command that reports `version_skew`; without the flag `act version` reads no repo and exits 0 anywhere. `--check-repo` is a guarded command: when a configured tracker remote holds a tracker this checkout has not cloned, it exits 3 with `tracker_not_checked_out` rather than `no_repo`.

---

### MCP tool surface

The server exposes one tool per CLI command, named `act_<verb>` (`act_init`, `act_create`, `act_list`, `act_show`, `act_update`, `act_close`, `act_dep_add`, `act_ready`, `act_search`, `act_log`, `act_doctor`, `act_version`), plus three composed tools. Read-only enforcement is a server-level concern: **no tool schema advertises a `read_only` parameter** (act-ca659d — it was a per-call advisory nothing enforced), and the server rejects any write tool when started with `--read-only`. Tool schemas leave `additionalProperties` unconstrained, so a client holding a cached older schema may still send `read_only`/`no_commit`/`isolated` and the call behaves as before. Transport is stdio. JSON Schema (concise) for each:

**Per-command tools.** Each `act_<verb>` accepts an object whose fields mirror the CLI flags (kebab-case becomes snake_case). Output is the command's `--json` body. Errors surface as MCP tool errors carrying `{code, kind, message}`.

**Advertised surface.** `tools/list` advertises the eight tools the work loop runs — `act_next`, `act_finish`, `act_block`, `act_file_blocker`, `act_list`, `act_show`, `act_create`, `act_update` (act-8a6536). The setup/diagnostic verbs (`act_init`, `act_version`, `act_doctor`, `act_log`, `act_search`, `act_ready`, `act_close`, `act_dep_add`) are **reachable through the identically-named CLI verbs** and remain dispatchable over MCP for clients holding a cached tool list; they are simply not advertised, because every advertised schema is re-read on every turn.

**`short_id` in responses.** Every payload that carries both `id` and `short_id` emits `short_id` **only when it differs from `id`** (act-8a6536). Ids are generated at the shortest-unique-prefix floor, so for an unextended id the two are byte-identical and emitting both duplicated the string on every row of every listing. Consumers MUST read an absent `short_id` as `short_id == id`, never as "no short handle".

**Composed tool: `act_next`**

Input:
```json
{"type":"object","properties":{
  "under":{"type":"string"}
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

Input: `{"id":"string","blocked_by":"string","reason":{"type":"string"}}`. Behavior: write one `add_dep` op with `type=blocks` from `id` to `blocked_by`. The "blocked" display state is derived automatically once the dep edge is present — no separate status op is written (that op was removed in act-018cb3). The `ops_written` label in the response is the literal string `dep-add`; the op type on disk is `add_dep`. Output:
```json
{"ok":true,"id":"...","blocked_by":"...","ops_written":["dep-add"]}
```

All composed tools mark themselves as the recommended path in their tool descriptions, surfacing `act_ready`/`act_update --claim`/`act_close`/`act_dep_add` as escape hatches.

## Errors, hooks, migration, bootstrap, compaction, tests

This section closes the remaining surfaces: structured error taxonomy, hook execution contract, bootstrap importer, op-schema migration, opportunistic compaction, edge-case behaviors, and the test plan. Together with the prior sections it makes the v1 surface implementable from one document.

### 1. Error handling

Every `act` command exits with a small, stable error code string. Under `--json`, errors are emitted on stdout as a single object; without `--json`, a one-line human message goes to stderr and stdout stays empty. JSON shape is uniform:

```
{"error": "<code>", "message": "<human>", "details": {...}}
```

`details` is optional and per-class. The `exit` column is the process exit code the binary returns with that error; several errors share one exit code, following the universal exit codes above (1 logical failure, 2 usage, 3 environment or not found), with `version_skew`/`bootstrap_timeout` (4) and `claim_lost` (5) the only rows outside 1–3.

| code                       | exit | human message format                                                       | details keys                                  |
|----------------------------|------|----------------------------------------------------------------------------|-----------------------------------------------|
| `not_in_git`               | 3    | `not in a git repository (run from inside a git worktree)`                 | `cwd`                                         |
| `act_not_initialized`      | 3    | `.act/ not found in <root>; run 'act init'`                                | `repo_root`                                   |
| `issue_not_found`          | 3    | `no issue matching '<query>'`                                              | `query`                                       |
| `id_ambiguous`             | 2    | `prefix '<p>' is ambiguous: <id1>, <id2>, ...`                             | `prefix`, `candidates[]`                      |
| `version_skew`             | 4    | `repo writer_version <N> exceeds this binary's writer_version <M>; upgrade act` | `max_op_version`, `writer_version`, `binary_version` |
| `claim_lost`               | 5    | `claim lost; winner=<node_id> at hlc=<hlc>`                                | `winner_node_id`, `winner_hlc`, `issue_id`    |
| `cycle_detected`           | 2    | `dep would create cycle: <a> -> ... -> <a>`                                | `path[]`                                      |
| `dep_not_found`            | 1    | `dep target '<id>' not found`                                              | `target`                                      |
| `hook_failed`              | 1    | `hook '<name>' exited <rc>; op unstaged`                                   | `hook`, `exit_code`, `stderr_tail`            |
| `import_invalid_jsonl`     | 1    | `bootstrap line <N> rejected: <reason>`                                    | `line`, `reason`, `source`                    |
| `remote_unreachable`       | 3    | `act bootstrap-worker: clone <url>: <reason>`                              | `url`, `stderr_tail`                          |
| `bootstrap_timeout`        | 4    | `act bootstrap-worker: clone <url> exceeded <N>s budget`                   | `timeout_seconds`, `url`                      |
| `target_not_empty`         | 2    | `act bootstrap-worker: <path> exists and is non-empty; pass --force to overwrite` | `target`                                |
| `blocked_by_external_dep`  | 2    | `<cmd>: <id> has <N> open external dep(s); clear with --ext-rm or override with --force` | `external_deps[]`               |
| `tracker_not_checked_out`  | 3    | `act: <what is missing>, but this repo's tracker exists at <url> (from <source>) — the queue is not checked out here. Recover with: <command>` | `repo_root`, `tracker_remote`, `source`, `recover` |
| `blocked_requires_dep`     | 2    | `act update: --status blocked: <id> has no blocked-by dep edge; add one first with \`act dep add <id> <blocker-id> --type blocks\`` | none |

**No state in this checkout vs. a tracker that is not checked out (act-a025ab).** `.act/` is gitignored, so with no `.act/` read commands print `act: no act state in this repo — this is normal in CI / fresh clones` and exit 0, and write commands exit 3 with `act_not_initialized`. act cannot tell on its own whether a tracker exists elsewhere — the only remote it records lives inside `.act/.git`, which is the missing thing — so an operator who keeps trackers in git remotes names the location once per machine: `$ACT_TRACKER_REMOTE`, else the first non-empty line of `$XDG_CONFIG_HOME/act/tracker-remote` (default `~/.config/act/tracker-remote`). The value is a git URL or path; `{repo}` becomes the host repo's directory name and a leading `~/` is expanded. Inside a linked git worktree `{repo}` is the main repository's directory name (the parent of git's common dir), not the worktree's own folder, so every worktree of a repo resolves to that repo's tracker (act-15ca2b). When that remote exists (a local path holding a git repo, or a URL `git ls-remote` answers within 10s) and this checkout has no `.act/` or no `.act/config.json`, every guarded command, read or write, exits 3 with `tracker_not_checked_out`; the message never calls the state normal. `act mcp` still starts in that state, but its tool calls (all but `act_version`) return an `isError` tool result carrying the same `tracker_not_checked_out` envelope instead of acting on an empty store (act-626391). `act init` in that state — no `.act/` config.json and no `.act/.git` to recover into — refuses with exit 3 and `tracker_not_checked_out`, printing the clone recovery, because a fresh init would start a second, divergent tracker; `act init --force-new` (MCP `act_init` argument `force_new`) starts a new tracker anyway. An `.act/` git repo missing only config.json is still recovered by a plain `act init`, and with no tracker remote configured (or none found) init is unchanged (act-ef5a69). When the remote does not exist the fresh-clone wording is unchanged. Unset, no probe runs. act ships no default location.

Retired codes (kept here so a reader who greps for one finds out why it is gone, rather than concluding the table is stale):

- `push_exhausted` (was exit 4) — retired by act-89a595. A push failure on an op that is already committed locally is a deferral, not a failure: the command exits 0, queues the commit to `.act/.pending-pushes`, and warns on stderr. See §"Auto-publish on write".

Rules:
- Every command MUST return exactly one error class on failure; no compound errors.
- `details` MUST be deterministic given inputs (no timestamps, no PIDs) so test goldens are stable.
- Human messages are single-line, no ANSI, no trailing period, lowercase first word except proper identifiers.
- `--json` errors go to stdout because agents parse stdout; stderr stays empty under `--json`.
- A flag the flag parser itself rejects — an unknown flag, a flag missing its value, a value of the wrong type for the flag — is `bad_flag` with exit 2, like a bad flag value the command rejects. When `--json` (or `-json`) appears anywhere in the arguments, before or after the rejected flag, the envelope goes to stdout and the parser writes nothing to stderr; its message is the envelope's `message`, prefixed with the command (`act list: flag provided but not defined: -nonsense`). Without `--json` the parser's message and the command's flag list go to stderr and stdout stays empty. `--json=true` (or any value the flag reads as true, such as `--json=1`) counts as `--json` here; `--json=false` counts as no `--json`, and when both are given the last one wins. A flag where the verb of `act state` or `act dep` belongs (`act state --json --nonsense`, `act dep --json`) follows the same rule: `bad_flag` with exit 2, the envelope on stdout under `--json` with the command's verb list as its `message`, the verb list on stderr without it. `-h` and `--help` are not errors: they print the flag list on stderr with or without `--json`, and no envelope.

### 2. Hooks contract

Discovery: only three filenames are loaded.

```
.act/hooks/create
.act/hooks/close
.act/hooks/claim
```

Any other filename in `.act/hooks/` is ignored. Future phases (`pre-commit-op`, `post-fold`, `post-compact`) are reserved names — `act` MUST refuse to load them in v1 with no warning, so a future binary can adopt them without breaking older repos.

Execution:
1. `act` resolves the hook by op-type (`create`/`close`/`claim`) and stats it. If absent or not executable, the hook is skipped silently. The hook runs **before the op file is written**: nothing exists under `ops/` while the hook is running, so a hook that refuses the op has nothing to roll back.
2. Hook is spawned synchronously with cwd = repo root.
3. Stdin: the op JSON, exactly one line, no trailing newline. Closing stdin signals EOF.
4. Env vars set: `ACT_OP_ID`, `ACT_OP_TYPE`, `ACT_ISSUE_ID`, `ACT_HOOK_PHASE=pre-commit-op`. No other `ACT_*` vars are set; future vars are additive.
5. Stdout and stderr are fully captured (bounded to 64KB each; overflow truncates and tags `details.truncated=true`). On success they are discarded; on failure stderr_tail (last 4KB) is included in the `hook_failed` error.
6. Timeout: 300s wall-clock by default, overridable per host with the `ACT_HOOK_TIMEOUT` environment variable (a Go duration: `20m`, `90s`; an unparseable or non-positive value falls back to the default rather than failing the write). On timeout `act` sends SIGTERM, waits 1s grace, then SIGKILL. Either way the hook is treated as failed — but it is reported as a TIMEOUT, not as a non-zero exit: the error names the limit that was hit and the variable that changes it, because a hook `act` killed and a hook that refused are different events and only one of them means "your gate said no". The limit is a host property, not a project one — the same gate on the same commit was measured at ~140s on one machine and 15m36s on another (act-8ee085) — which is why it is an environment variable and not a value in the tracker.
7. Non-zero exit: `act` emits `hook_failed` and writes no op file at all. No commit is created. Caller exits with code 1.

Why the hook runs before the write (act-8ee085): it used to run with the op file written and `git add`-staged, and a failing hook rolled that back by deleting the file. That rollback is not atomic with respect to anything else reading the working tree — a sweep that commits uncommitted op files (`git add -- ops`, as the fleet's act-sync job does) landing inside the hook's window committed an op the hook was in the middle of refusing, leaving it in HEAD and absent from the working tree, so `act show` and every consumer of committed state disagreed silently. A hook that runs a test suite makes that window minutes wide. Writing after the hook passes removes the window rather than policing it. The hook's own contract is unchanged: it never had access to the op file, only to the op JSON on stdin and the `ACT_*` environment.

Where a rollback is still possible — the op file is written and a later step (the commit) fails — `act` guarantees the working tree and HEAD do not disagree about it: the op file is withdrawn to `.act/.failed-ops/`, and if HEAD already tracks it (something committed it in the interval) `act` commits the removal itself (subject `act: withdraw <type> op for act-XXXX (commit failed)` — deliberately not the `act-op:` form, since it retracts an op rather than recording one) rather than leaving a dirty deletion for a blind sweep to publish.

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
- `writer_version` — the op-format version of the binary that produced the op. Informational on read: nothing in the fold compares it.

There is no reader gate on `writer_version`. The fold never compares it (see "Version fields on read" under the fold pseudocode): an op from a newer writer is read and applied like any other, and no read or write command refuses with `version_skew` because of it. This is deliberate — an older reader must keep answering rather than crash on a tracker a newer binary has written to. Writer-version skew is reported only on request: `act version --check-repo` walks `.act/ops/` and exits 4 with `version_skew` when any op's `writer_version` is newer than this binary's. What does stop an older reader is a different `op_version` or `schema_version`, which fails the envelope's equality check and aborts the fold.

Fold dispatch: the fold loop calls

```
state = apply_dispatch(op.op_type)(state, op)
```

Dispatch is on `op_type` alone: `fold.ApplyDispatch` maps each type the fold applies to one pure apply function, and returns nothing for a type it has no rule for (an unknown type, or the parse-only legacy `redact`), which the fold skips. There is no dispatch on `op_version`. An op whose `op_version` is not the one this binary writes never reaches dispatch at all: it fails the envelope's equality check and the fold aborts, as described above.

Migration ops: `act migrate` is the only command that writes a `migrate` op.

**Synopsis:** `act migrate --from <N> --to <M>`

**Flags:** `--from` (int, source `op_version`) and `--to` (int, target `op_version`). There are no others — no `--dry-run` and no `--json`. The command is not listed in `act help`.

**Behavior:** looks up the migration registered for exactly `(from, to)`, then walks `.act/ops/` for ops at `op_version` `<N>`. Each one is passed through the migration's transform, and the ops the transform returns are written as new op files stamped after every existing op. One `migrate` op per affected issue (not one global) is then written to record the transition. Its envelope payload:

```
{"from_version": 1, "to_version": 2, "transform_id": "<migration description, or v1-to-v2>"}
```

The transform is referenced by name, not embedded as code. On fold a `migrate` op changes nothing a reader sees; it only records the transition on the issue's internal state.

**Output:** always one JSON object on stdout. Success is `{"migrated_issues": <count>, "wrote_ops": <count>}`; a failure is `{"error": "<code>", "message": "..."}`.

**Exit codes:** 0 on success, including nothing to migrate; 1 on a read, transform or write failure; 2 on an unknown flag, or `bad_input` — `--from` or `--to` missing or not positive, or `--from` not less than `--to`; 3 outside a git working tree (`not_in_git`); 5 `migration_not_found` when no migration is registered for `(from, to)`. No migration is registered in this version, so every well-formed `act migrate` call today exits 5 and writes nothing.

A binary older than the migration does not answer `version_skew`: an op carrying a newer `op_version` fails the envelope check, so the fold aborts with an error naming the op file and `op_version` (`act list` exits 1), exactly as for any other op whose `op_version` is not the one the binary writes.

### 5. Compaction

**Not wired in.** The engine described below exists as `compact.Run` in `internal/compact`, but no command, write path or MCP tool calls it, so a running `act` never compacts: no writer checks the trigger, no snapshot is written, and the fold never seeds from one. `compact` is also not in the `op_type` enum. Read this section as the design the engine was built to, not as behavior.

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
6. **Commit.** One commit: snapshot file + compact op (+ deletions if step 4 ran). The engine makes one commit per run, covering every issue it compacted, with subject `act-compact: <N> issues`.
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
- **No squash, and no `version_skew` refusal on write.** No command squashes `act-op:` commits: each op write is its own commit in the nested `.act/` repo and stays that way. A tracker holding an op from a newer writer still accepts writes — `act update` and `act close` commit as usual while `act version --check-repo` reports the skew.
- **`act doctor --check unknown-op-version`** is about `op_version`, not `writer_version` (see the doctor check table): it emits one finding per op file with a newer `op_version`, naming the file. It does not report writer-version skew. Plain `act doctor` reports it too: such an op aborts the fold, so the run returns the `fold-error` finding followed by the findings of the fold-free read-only checks (`unknown-op-version` among them; the `fold-error` row of the doctor check table lists them), and the checks that need the fold do not run.

### 7. Test plan

Test code lives under `internal/.../*_test.go` (or equivalent if the Bun spike flips the language). Goldens live under `internal/fold/testdata/golden/`. Fuzz corpora under `internal/fold/testdata/fuzz/`. CI runs `go test ./...` on every PR.

#### 7.1 Property tests

There is no property-testing library in the build. The property tests are ordinary Go tests in `internal/fold` that loop over fixed seeds of `math/rand`, so every run draws the same cases and a failure prints the seed that produced it. They run under plain `go test`.

- **`TestPropertyLWWPermutationInvariance`** (`lww_test.go`) — 100 seeds, 12 random ops each on a single issue. Folding the ops is compared, byte-for-byte as canonical JSON, against folding five shuffles of the same ops.
- **`TestPropertyFoldDeterminism`** (`property_test.go`) — 50 seeds, 10 to 30 random ops each. The ops are written to disk and folded, then written in three shuffled orders and folded again; the canonical render of the whole result must be byte-identical.
- **`TestPropertyHLCMonotonicityPerNode`** — one node emits 1000 HLCs while its wall clock jitters forward and backward; each must be strictly greater than the one before.
- **`TestPropertyAddRemoveDepIdempotent`** — a random run of `add_dep` / `remove_dep` ops on one edge; after the fold the edge is present exactly when the op with the largest HLC is an `add_dep`.
- **`TestPropertyClaimEarliestWinner`** — several competing `claim` ops with random HLCs and node ids; the folded assignee is the claim with the smallest HLC.
- **`TestPropertyGeneratorCovers12OpTypes`** and **`TestPropertyGeneratorReproducible`** check the op generator itself: it emits every op type it knows, and the same seed yields the same ops.

The permutation tests shuffle arbitrary generated ops, not a hand-picked commutative-disjoint set: the fold sorts by HLC and op hash before applying, and that sort is what the tests pin.

#### 7.2 Golden tests

One directory per case under `internal/fold/testdata/golden/<case>/`, each holding `before.json`, `op.json` and `after.json`. `TestGoldenApply` (`internal/fold/golden_test.go`) loads the before-state, applies the op through the real apply dispatch, and compares the rendered state to `after.json` byte-for-byte as canonical JSON. A case is named for its op type. Golden cases:

```
add_accept, add_dep, add_external_dep, claim, close, create, import, migrate,
remove_accept, remove_dep, remove_external_dep, reopen, set_accept, tombstone,
unclaim, update_field, update_field-stale
```

That is every op type in the `op_type` enum except `redact` (parse-only, no apply path to exercise), plus `update_field-stale`, an extra case for an `update_field` that loses last-writer-wins. `TestGoldenApply` fails if an op type in `op.ValidOpTypes` other than `redact` has no case directory, so adding an op type requires adding its golden. There is one op_version (1), so cases are not split by version.

#### 7.3 Fuzz

- **`FuzzFoldDeterminism`** (`internal/fold/fuzz_test.go`) — a Go native fuzz target. The input bytes are decoded into at most 24 well-formed ops (inputs are cut to 256 bytes); the ops are folded, shuffled by a PRNG seeded from the input, and folded again, and the two canonical renders must be byte-identical. Both folds are cold — there is no warm-versus-cold comparison. The target has four in-code seeds and two committed corpus files under `internal/fold/testdata/fuzz/FuzzFoldDeterminism/`.

Plain `go test ./...` — which is what CI runs — executes the fuzz target against those seeds only, as a regression test. Actual fuzzing is a manual step, `make fuzz` (`go test -fuzz=Fuzz -fuzztime=10s ./internal/fold/`); a failing input found that way is minimized by the Go fuzzer and written to the corpus directory, to be committed by hand.

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
- `act_block` writes a dep edge (blocked-by) so the issue displays as blocked; no separate status op is written.

#### 7.6 Doctor coverage

For each check (`stale-git-lock`, `orphan-close`, `orphan-ops`, `dangling-deps`, `time-travel`, `cycle`, `unknown-op-version`, `index-malformed`, `index-divergence`, `index-schema`, `status-vs-oplog`, `gitignore-effective`, `nested-exclude`, `phase2-reconciliation`, and `fold-error`, which has no pass of its own):
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
`act_block` writes exactly one op — an `add_dep` op with `type=blocks` — and no status op (blocked is derived from the edge; see the `act_block` tool above). It MUST `git add` and commit that op in a single git commit. If the stage or commit fails, the op file MUST be unstaged and deleted so no op is left in the op log, and the underlying error class MUST be surfaced to the caller.

### 5.D.3 `stderr_tail` is last 4096 bytes UTF-8-trimmed; `truncated` reflects only the 64KB cap
`stderr_tail` is the last 4096 bytes of captured stderr, trimmed to the nearest valid UTF-8 boundary. `details.truncated` MUST be `true` if and only if the 64KB capture cap was hit, independent of the 4KB tail trim.

### 5.D.5 MCP test 7.5 budget reconciled with jitter math; exactly 3 claim attempts via injected clock
The base delay sum is `100ms + 400ms + 1.6s = 2.1s`; with per-attempt uniform jitter `[0.75x, 1.25x]` the achievable sleep range is `[1.575s, 2.625s]`. MCP E2E test 7.5 MUST inject a deterministic clock AND a deterministic jitter source so the test asserts exact, reproducible values: jitter sources MUST be seeded to produce `(1.0x, 1.0x, 1.0x)` (no jitter) and total elapsed sleep MUST equal `2.1s ± 50ms`. Per-attempt work time (fold + git ops) is excluded from the budget by using the injected clock to advance only on sleep; exactly 3 claim attempts MUST be observed.

## Coordination plane: Phase 2 config schema

Phase 2 (docs/coordination-plane-design.md, Phase 2 design) introduces a
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
| `act.fetchTimeoutSeconds` | `10` | Wall-time cap for an upstream `git fetch`: a fetch exceeding the budget is aborted (the process is killed) and surfaces as a fetch failure. Unset ⇒ unbounded; `act remote enable` writes the 10s default (tickets 4 / 5). |
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

The default TTL is 5 seconds. The `act.readCacheTTLSeconds` key in `.act/.git/config` overrides the default window per-repo: a positive integer sets the freshness window in seconds (a larger value widens it, serving on-disk state longer between fetches; a smaller value narrows it). An unset, non-positive, or unparseable value falls back to the 5-second default. The bypass paths below ignore the window entirely regardless of its configured value.

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

## `act bootstrap-worker --from-cwd` (worker-cwd mode, act-40fce0)

`act bootstrap-worker --from-cwd <orchestrator-path> [<target-path>]` is the
source/target inversion of the default cwd-source mode. The **worker** runs
it from inside its freshly-created worktree, naming the **orchestrator**'s
repo root (or `.act/` directory) as the source; the target defaults to the
process cwd when the positional `<target-path>` is omitted. It exists because
a worktree created *during* an Agent dispatch does not exist when the
orchestrator would otherwise run `act bootstrap-worker <target>` — so the
orchestrator cannot bootstrap a not-yet-existing target, and the only
previous workaround was a raw `cp -r <orchestrator>/.act .` from inside the
worktree.

### Behavior

- Resolve the source: `<orchestrator-path>` may be a host repo root or a
  `.act/` directory; both resolve to the orchestrator's `.act/` tree. Source
  must have a nested `.act/.git` or exit 3.
- Resolve the target: `<target-path>`, defaulting to cwd. Refuse a non-empty
  target `.act/` unless `--force` (exit 2, envelope `target_not_empty`).
  Refuse a source/target that resolve to the same `.act/` (exit 2,
  `bad_flag`).
- Copy the source `.act/` op log + `config.json` + `snapshots/` + `imports/`
  + nested `.act/.git` into a private staging dir, then atomic-rename to
  `<target>/.act/`. The host `hooks/` subtree is omitted (same rationale as
  the other modes).
- **The live `index.db` (and its `-wal` / `-shm` / `-journal` sidecars) is
  NOT copied.** After the rename, the index is rebuilt locally from the
  copied op log (`index.Rebuild`). This is the load-bearing difference from
  `cp -r`: copying a live, orchestrator-open SQLite file produced the
  fragile concurrent-SQLite / stale-index interaction behind the
  concurrent-worker silent-data-loss bug. The index is a pure derived
  cache, so a local rebuild is always correct.
- Round-trip validate via a `ready`-equivalent; tear the target down on
  validation or index-rebuild failure.

`--from-cwd`, `--from-remote`, and the default cwd-source path are mutually
exclusive at the flag layer.

### Op-write persistence guarantee

Independent of bootstrap mode, every op write (`act create`, `act close`,
`act dep add`, and the other write subcommands) MUST NOT return a synthetic
success when the op cannot be confirmed durably persisted to a location
`act harvest` will see. After writing the `.act/ops/<op>.json` file the
writer fsyncs the file, fsyncs the containing directory, and **reads the
file back**, requiring byte-equality with what was written; a read-back
failure or mismatch surfaces a loud `write_failed`-class error (non-zero
exit) and removes the unconfirmed file, never "Created act-XXXXXX". Silent
success that later vanishes is the worst failure mode and is the class this
guarantee closes.

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
(always emitted). On a run where `phase2-reconciliation` did not run —
a failed fold, or `--check` naming another check — `remote_reachable`
is `true` and the other four fields hold their zero values (`0`, `""`):
nothing was probed, so nothing is reported unreachable. The five fields:

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
an earlier design note claimed Mode A was the degenerate case of
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
   reference orchestration flow does implicitly today: each
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

## Branch discovery (Mode B)

This section documents the **as-built** behavior. Surfaced by act-d264
following the act-334e abstractions review: the push-asymmetry framing
in an earlier design note had Mode B workers pushing to "their
assigned branch" while the orchestrator merges that branch into main
on its own cadence. The framing assumes the orchestrator can answer
the question "which branch is working issue X?" — but neither the spec
nor the implementation defines a surface that answers it.

**Current status: NOT DESIGNED.** There is no act surface — CLI, MCP,
op envelope, config key, or doctor check — through which an
orchestrator can ask act for the branch ref working a given issue. The
field set on the `claim` op is `{assignee: string}` and nothing else
(`internal/op/payloads.go` `ClaimPayload`); there is no `branch`,
`worktree`, or `ref` slot, populated or reserved. The `assignee` is
free-form and by convention holds the writer's `node_id` (per
`internal/cli/util.go` `InClaimWindowForNode` comparing
`p.Assignee != nodeID`), which identifies the host that wrote the
op — not the working branch.

### Data sources currently available

The four places an orchestrator might look for branch information,
and what each actually carries:

- **Op envelopes (`.act/ops/<issue>/**/*.json`).** The `claim` op's
  payload is `{assignee}`; the envelope adds `node_id`, `hlc`,
  `op_type`, `op_hash`. None of these are branch refs. The `assignee`
  is a node id by convention, not a ref. No optional field, no notes
  channel, no extension point in the schema carries branch.

- **`act show <id>` / `act show <id> --json`.** Surfaces the folded
  state: `id`, `title`, `status`, `assignee`, `priority`, `type`,
  `parent`, `description`, `accept`, `deps`, `created_at`,
  `claimed_at`, `commits`, plus `commit_marker` for the canonical
  `Act-Id:` trailer (`internal/cli/show.go formatShowFields`,
  `ShowJSON`). No branch, ref, or worktree field appears in either
  the human or JSON output. The MCP `act_show` tool returns the same
  shape.

- **`commits` field on `act show`.** Populated by
  `gitops.WorkCommitsForIssue` (`internal/gitops/gitops.go`), which
  runs `git log --all --extended-regexp --grep=<pattern>` against the
  host repo for either the historical `(act-<hex>` subject form or
  the `Act-Id: act-<hex>` trailer form (§"Marker placement" in
  `docs/coordination-plane-design.md` v2.1). This is the closest
  thing to a branch signal act exposes today — but only *after* the
  worker has committed with the marker. An orchestrator that has a
  SHA from `commits` can run `git branch --contains <sha>` to
  enumerate branches reachable from that commit. Two caveats:
  (a) `--all` returns commits across every ref on the host repo
  including main, so reachability of a marker SHA from main does
  not mean the worker is still on a branch; (b) for an
  in-progress claim that has not yet written its first work commit,
  `commits` is empty and the SHA-to-branch path is unavailable.

- **`.act/.git/refs/` (the nested coordination-plane repo, Phase 2).**
  The nested repo's branch ref is internal to the act state machine
  — it advances on every op commit, fast-forwards under HLC ordering,
  and bears no semantic relationship to the host repo's working
  branch (`docs/coordination-plane-design.md`, Phase 2 design — the only
  mutable thing in `.act/.git` is the branch ref). The nested repo's branch is
  `main` (or whatever `.act/.git/HEAD` names; see §"Coordination plane:
  Phase 2 config schema") regardless of which host-repo branch is
  carrying the worker's code commits. Reading `.act/.git/refs/` does
  not answer the question.

- **`act.role` config key.** Set to `worker` by
  `act bootstrap-worker --from-remote` (§"Coordination plane: Phase 2
  config schema", `act.role` semantics) and to `orchestrator` by
  `act remote enable`. The role marker partitions sync responsibility;
  it does not record the worker's host-repo branch. A worker's
  `.act/.git/config` has no field naming the host-repo branch it is
  working on, and `act bootstrap-worker` neither requires nor records
  one.

### Naming conventions

The Claude Code reference implementation uses a fixed convention:
worktrees live at `<repo>/.claude/worktrees/agent-<hex>/` and the
branch is `worktree-agent-<hex>` (verifiable on any active orchestrator
session via `git worktree list`). The convention is **opaque to act**.
The hex is the harness's worktree id, not the issue id. One worker
working one issue, one worker working a sequence of issues, and two
workers working unrelated issues all draw from the same hex namespace.
There is no inverse: given an issue id, no construction recovers the
branch name.

This is the design a typical orchestrator command flow uses: the
orchestrator dispatches a worker with `isolation: "worktree"`, the harness mints the worktree
and branch under the `agent-<hex>` / `worktree-agent-<hex>` shape, the
orchestrator instructs the worker to **"Return your branch name in
your final report,"** and the orchestrator extracts the branch from
the report on completion. The branch-discovery path is the *report
text*, not any act surface. Any orchestrator that cannot read the
worker's final report (e.g. the worker crashes mid-flight, the
harness has no report channel, or the orchestrator wants to query
mid-execution) has no fallback.

### Guarantees

- **No strong guarantee.** Nothing in the act spec or implementation
  binds an issue id to a branch ref.
- **No weak guarantee.** No optional or best-effort field carries
  branch information either.
- **No naming convention act enforces.** `worktree-agent-<hex>` is a
  harness-side convention with no act-side parser, validator, or doc
  claim. A different harness (a beads-based harness, plain `git worktree
  add` by hand, a future scheduler) could pick any branch name and
  act would have no way to notice.

### Recommended orchestrator query path

A Mode B orchestrator implementing dispatch today MUST track the
branch-to-issue mapping out-of-band. Specifically:

1. **Record the branch at dispatch time.** When the orchestrator
   creates a worktree (via `isolation: "worktree"`, `git worktree
   add`, or any other mechanism), it knows the branch name it
   chose — record `(issue_id, branch_ref)` in its own state
   (dispatch log, in-memory map, scheduler database). This is the
   only fully reliable path.
2. **Confirm via the worker's final report.** When a dispatched
   worker returns, parse its report for the explicit branch line.
   This is what an orchestrator typically does today; treat it as
   confirmation of the dispatch-time record, not as the source of truth.
3. **Fall back to commit-marker grep when the dispatch record is
   lost.** Given an issue id, run `git log --all --grep='Act-Id:
   act-<hex>'` (or equivalently `act show <id> --json | jq
   .commits`) to find work commits, then `git branch --contains
   <sha>` to enumerate branches reachable from each commit. Filter
   out main and any already-merged branches. This is best-effort:
   it works only after the worker has written at least one work
   commit, returns zero results during the gap between claim and
   first commit, and returns ambiguous results when a worker has
   worked multiple issues on the same branch.

Path 1 is the supported design. Paths 2 and 3 are fallbacks the
orchestrator implements on top of primitives act exposes for other
purposes.

### Forward path

Closing this gap is a separate work item. Three shapes have been
sketched in the act-d264 ticket; this section documents the trade-offs
of each so a future implementer can choose with full context.

- **(a) Convention.** Bake a branch-naming convention into the skill
  and into act tooling so an orchestrator can construct the branch
  name from an issue id (e.g. `worktree-act-<hex>`). Trade-off:
  rigid — fails the moment one issue is worked across two branches,
  two issues across one branch, or a non-Claude-Code harness picks a
  different convention. The current `agent-<hex>` convention uses
  the harness's worktree id precisely because mapping issue-to-branch
  one-to-one is wrong for the cases above.
- **(b) Structured field on the claim op.** Add a `branch` (or
  `worktree`) field to `ClaimPayload` and surface it via `act show
  --branch` and `act show --json`. Trade-off: requires an op-schema
  migration (§4 "Op-schema migration"), a new write-time validator,
  a doctor check for branch-unset-but-status-in-progress, and a
  decision about whether the field is required or optional. Stays
  inside the existing op-fold contract (the field is set-once at
  claim time, LWW like other claim-op writes). The natural surface
  shape is a new `--branch <ref>` flag on `act update --claim` /
  `act_next`, written into the claim envelope's payload and
  propagated to the folded state.
- **(c) Notes channel.** Add a free-form `notes` field to op
  envelopes that workers can write conventional strings into
  (`worktree=worktree-foo`) and orchestrators can grep. Trade-off:
  loose — no schema, no validation, no surface; consumers have to
  agree on grep patterns out-of-band. This is the worst of the
  three for a coordination contract and is documented here only
  for completeness.

The likeliest shape is (b) — structured field — because it stays
inside the deterministic-fold contract (§6), composes with existing
config-key plumbing for default-branch policies, and gives doctor a
concrete invariant to check. A spec section on the branch field would
live near §"Op type payloads" and §"`act update <id>`" in this
document.

This section will be promoted from "currently undefined" to a
specification as and when the implementation lands. Until then, the
explicit answer for any Mode B orchestrator asking "how does act
expose the branch working a given issue?" is: **it does not; the
orchestrator owns this concern out-of-band.**
