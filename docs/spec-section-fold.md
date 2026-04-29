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
