# Brief Rebuttals (review-1 → v2)

## Challenge 1: File-count explosion in `.act/ops/`
**Disposition:** Incorporated
**Action / Rebuttal:** Shard ops as `ops/<id>/<yyyy-mm>/...` to bound per-directory entries. Compaction becomes opportunistic (auto-fires above 50 ops or 30 days since last snapshot) on any writer holding the lock. Budget: <2k total files in `.act/` at 1-year-old project.

## Challenge 2: Unbounded fold cost on cold start
**Disposition:** Incorporated
**Action / Rebuttal:** Persist a fold checkpoint keyed by the git tree-hash of `.act/ops/`. Reuse SQLite cache when tree-hash matches; otherwise fold only changed paths. Documented worst-case fold latency budget.

## Challenge 3: No op-schema migration story
**Disposition:** Incorporated
**Action / Rebuttal:** Every op carries `op_version` and `schema_version`; fold dispatches per-version. `act migrate` writes new ops representing upgrades rather than rewriting history. Op schema is a stability surface co-equal with the CLI.

## Challenge 4: Wall-clock LWW unsafe under skew
**Disposition:** Incorporated
**Action / Rebuttal:** Replace wall-time ordering with hybrid-logical clock (HLC) carried in op payload; tiebreak by op-hash. Ops with implausible HLC deltas (>5 min from local) are refused at write time.

## Challenge 5: Silent claim-loss
**Disposition:** Incorporated
**Action / Rebuttal:** `--claim` becomes blocking-and-verifying: write op, `git pull --rebase`, re-fold the issue, exit non-zero with structured `{"claimed": false, "winner": "..."}` if local op didn't win. Adds `--wait` for poll-to-stability.

## Challenge 6: Claim re-check protocol under-specified
**Disposition:** Incorporated
**Action / Rebuttal:** Protocol is now explicit: (1) write op, (2) `git pull --rebase` from configured remote, (3) fold, (4) report win/loss, (5) push only on win unless `--push-always`. `--isolated` flag opts out of remote fetch for offline use.

## Challenge 7: Auto-commit vs leave-to-agent
**Disposition:** Incorporated
**Action / Rebuttal:** Default is auto-commit with `act-op: <id> <op-type>` message prefix. `act compact` collapses contiguous `act-op:` commits before push. `--no-commit` flag for batching agents. Durability beats history aesthetics.

## Challenge 8: `act doctor` fragile against squash/rebase
**Disposition:** Incorporated
**Action / Rebuttal:** Doctor now scans both commit messages and the diff of `.act/ops/**` for close-ops; treats any commit touching an issue's ops directory as evidence. Snapshots carry a `closed_by_commit` reverse index for symmetric verification.

## Challenge 9: 4-hex-prefix ID collisions
**Disposition:** Incorporated
**Action / Rebuttal:** Internal IDs are full hashes; CLI displays the shortest unique prefix per session (git-style). On collision, all displays for tied IDs lengthen one hex at a time. Creates that would tie an existing already-quoted prefix are rejected.

## Challenge 10: Title rename behavior undefined
**Disposition:** Incorporated
**Action / Rebuttal:** ID is hash of `(create-op payload + random nonce)`, never of title. Title is a mutable field; renames are ordinary update ops. Stated explicitly in data model section.

## Challenge 11: Missing `act search` and `act log`
**Disposition:** Incorporated
**Action / Rebuttal:** Add `act search <query> [--in title|desc|all]` (FTS5 over the SQLite index) and `act log <id>` (renders op stream). Drop `act dep rm` from v1 surface; rare op exposed as `act update --dep-rm`.

## Challenge 12: `act compact` is vestigial as a user command
**Disposition:** Incorporated
**Action / Rebuttal:** Remove `act compact` from v1 surface. Compaction runs automatically on threshold (see Challenge 1). `act doctor --compact` is the manual escape hatch.

## Challenge 13: MCP 1:1 with CLI is wrong default
**Disposition:** Incorporated
**Action / Rebuttal:** Keep 1:1 tools but add composed convenience tools: `act_next` (ready+claim+show), `act_finish` (close+commit-marker), `act_block` (status=blocked + add-dep). Composed tools marked as recommended path in tool descriptions.

## Challenge 14: Bun/TS vs Go dismissed too fast
**Disposition:** Partially
**Action / Rebuttal:** Decision is no longer asserted; brief now requires a 1-day spike comparing Go and Bun on the cross-compile matrix and op-fold determinism. Default remains Go pending the spike result, justified by static-binary maturity for the 5-target matrix.

## Challenge 15: Distribution ignores op-schema version skew
**Disposition:** Incorporated
**Action / Rebuttal:** Every op carries `writer_version`. On read, if any op's `writer_version` exceeds reader's known max, exit with structured "upgrade required" error. `act version --check-repo` verifies compatibility. Cowork plugin manifest pins a specific binary version.

## Challenge 16: Hooks deferral is questionable
**Disposition:** Incorporated
**Action / Rebuttal:** Ship a minimal file-hook surface in v1: `.act/hooks/{post-create,post-close,post-claim}` as plain executables run synchronously after the corresponding op is committed. Pubsub/event-bus stays deferred.

## Challenge 17: Search not in v1 nor in deferrals
**Disposition:** Incorporated
**Action / Rebuttal:** Resolved by Challenge 11 — search and log are pulled into v1 explicitly. The deferred list no longer carries this ambiguity.

## Challenge 18: Bootstrap incoherence
**Disposition:** Incorporated
**Action / Rebuttal:** Bootstrap with a 50-line shell script writing `_generated/projects/act/issues.jsonl` — the simplest possible op-log. v0.1 of `act` ships an importer that consumes that JSONL on first run, dogfooding the op-log primitive from line one.

## Challenge 19: Beads-anchoring in data model
**Disposition:** Partially
**Action / Rebuttal:** The spec phase will produce a parallel "fresh-eye" model `(id, title, body, status, deps, claim)` and compare on a concrete 3-task workflow. Brief retains current taxonomy as the working baseline; each retained Beads field must be justified in the spec or dropped. Defer full re-derivation to spec stage because the comparison artifact is itself spec-scope work.

## Challenge 20: Testing strategy is a smoke test
**Disposition:** Incorporated
**Action / Rebuttal:** Test plan now includes (a) property tests on op-fold permutation invariance, (b) golden tests per op type, (c) fuzzer for random op sequences asserting determinism, (d) MCP end-to-end via fake stdio client, (e) git rebase contention tests.

## Challenge 21: DoD requires human sign-off in three places
**Disposition:** Incorporated
**Action / Rebuttal:** Three CI jobs containerize each environment (CC laptop, CC on the Web, Cowork). Agent runs them and reports PASS/FAIL. Human signs off only on the final tag.

## Challenge 22: "Reproducible state" is undefined
**Disposition:** Incorporated
**Action / Rebuttal:** Reproducibility is now defined operationally: for any sequence of supported queries, two folds of the same git tree return byte-identical JSON output. SQLite file bytes are explicitly not reproducible; query results are. Tested in CI.

## Challenge 23: No story for `.act/` size growth
**Disposition:** Incorporated
**Action / Rebuttal:** Retention policy: closed-issue op directories collapse to a single terminal snapshot after 30 days. Deleted issues retain only a tombstone. Documented size budget: ~1KB per closed issue at steady state.

## Challenge 24: Doctor's job is too narrow
**Disposition:** Incorporated
**Action / Rebuttal:** Doctor becomes a battery: `orphan-close`, `orphan-ops`, `dangling-deps`, `time-travel`, `cycle`, `unknown-op-version`, `index-divergence`. Selectable via `--check <name>`; default runs all.

## Challenge 25: No defined behavior for deletion
**Disposition:** Incorporated
**Action / Rebuttal:** Define a `redact` op that overwrites prior op payload contents in derived snapshots and retains a tombstone in the op log. For true secret removal from git history, document `git-filter-repo` as the expected escape hatch. Policy stated explicitly.
