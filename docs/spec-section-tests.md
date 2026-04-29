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
