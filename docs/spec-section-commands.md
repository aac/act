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

**Behavior:** Builds a `create` op payload, hashes `(payload || nonce)` to derive the new full issue id `act-<16-hex>`, writes the op file at `.act/ops/<id>/<yyyy-mm>/<iso>-<hash6>-create.json`, runs the `post-create` hook, then op-commits unless `--no-commit`.

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

**Synopsis:** `act doctor [--check NAME] [--fix] [--json]`

**Flags:**
- `--check NAME` (repeatable). Default: run all.
- `--fix` (bool). Auto-remediate where safe (per check, below).
- `--json` (bool).

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
