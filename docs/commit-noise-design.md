# Commit Noise Design Note — act-6018

_2026-05-10. Analysis only; no production code changed._

## Quantitative Table: What Does an Issue Cost in Commits?

Since the v0.1.0 tag, this repo has **107 commits**. Breaking them down:

| Category | Count | % of total |
|---|---|---|
| `act-op:` auto-commits (claim, close, create, etc.) | 55 | 51% |
| Old-format claim commits (`act-act-XXXX: claim`) | 19 | 18% |
| **Total act bookkeeping** | **74** | **69%** |
| Work commits with `(act-XXXX)` marker | 23 | 22% |
| Other commits (infra, docs, no marker) | 10 | 9% |

Note: two commit formats exist because the canonical `act-op:` subject was standardized mid-stream (act-d3a5). Claims before that fix used `act-act-XXXX: claim <node>`. Both formats are pure bookkeeping; the 51% figure in early drafts missed the old-format rows.

**69% of all commits are pure bookkeeping.** Humans reading `git log` see roughly one work commit for every three act-op commits.

Tracing six representative issues:

| Issue | claim commits | work commits | close commits | total per issue |
|---|---|---|---|---|
| act-5467 (commit_marker surface) | 1 | 1 | 1 | **3** |
| act-63a1 (dep add aliases) | 1 | 1 + 1 merge | 1 | **4** |
| act-acd9 (help errors topic) | 3 (retried claim) | 1 + 1 merge | 1 | **6** |
| act-6fca (prefix resolution) | 1 | 1 + 1 merge | 1 | **4** |
| act-fdb2 (claim no-upstream) | 2 (retried claim) | 1 + 1 merge | 1 | **5** |
| act-8dcd (ambiguous-id exit) | 1 | 1 + 1 merge | 1 | **4** |

Across 18 closed issues, 74 act bookkeeping commits (55 new-format + 19 old-format claims) = **~4 act-op commits per issue** on average. Add the work commit and a merge commit: **~6 commits per issue** in total, of which 4 are pure overhead.

Claim retries (act-acd9, act-fdb2) each add commits. Failed or multi-attempt claims are not unusual in parallel-agent sessions.

---

## Is the Per-Op-Commit Model Load-Bearing?

The architectural thesis: op-log files in `.act/` are the ground truth; commits make them replicate. Three properties depend on this:

1. **Audit trail.** The sequence of ops (who claimed, when, what HLC) lives in `.act/` op files, not in commit messages. The commit is just the transport vehicle.
2. **Multi-writer correctness.** `WriteOpAndAutoCommit` ties write + commit atomically; a partially-written op that crashed before committing is excluded from the log. This is about op-file integrity, not commit message content.
3. **HLC ordering.** The op-log's HLC comes from the op file timestamp; git commit time is irrelevant.

**Conclusion:** the op-log model does not require one-commit-per-op. Commits are the sync unit, not the ordering unit. Bundling multiple ops into one commit preserves all three properties, at the cost of slightly coarser "what committed when" granularity in git (but not in the op-log, which retains HLC per-op).

The per-op-commit design is an **implementation convenience, not a load-bearing constraint**.

---

## Option Evaluation

### Option 1: Bundling with Periodic Flush (recommended)

**Concept:** `act` writes ops to `.act/` files but defers `git commit` until the session calls `act flush` (or a mode flag sets auto-flush on close). Default behavior in production mode: ops accumulate, one commit at session end groups them all.

**Implementation cost:** Medium. `WriteOpAndAutoCommit` in `internal/store/` becomes `WriteOp` + conditional commit; `act flush` is a new command (~100 lines). Config flag `--no-auto-commit` or `ACT_MODE=production`-analog. Estimated ~200 lines across 3 files: `internal/store/store.go`, `cmd/act/flush.go`, `internal/config/config.go`.

**Audit-trail preservation:** Full. Op files retain per-op HLC timestamps. Flush commit message can enumerate which ops it contains (`act-op: flush 5 ops (claim act-6018, close act-5467, ...)`). Nothing lost from the ground truth.

**Multi-writer correctness:** Safe. The race is at the git index level, not the op-log level. Two writers each accumulate ops locally, then one rebases before flushing. The op-log's append-only structure still resolves correctly. The claim protocol's idempotency means a re-flushed claim is fine.

**Behavior under merge/rebase:** Improved. Fewer commits means fewer rebase conflicts. The only sensitive git state is the op files themselves; bundling reduces the rebase surface.

**UX impact:** Agents must learn that ops are not committed until `act flush`. The canonical loop in CLAUDE.md would gain a step. This is a meaningful mental model change. Mitigated by: keeping auto-commit as the default in dogfood/dev mode, making bundling opt-in via config or flag.

---

### Option 2: Separate-Branch Ops (`refs/act/ops`)

**Concept:** All `act-op:` commits go to a separate git ref. Main branch only sees work commits.

**Implementation cost:** High. Every write op must switch git HEAD, commit to the side ref, switch back. Worktrees make this slightly less painful but it's still ~400 lines and significant test surface. Breaks tooling that assumes a single working branch.

**Audit-trail preservation:** Full (ops are still committed, just on a different ref).

**Multi-writer correctness:** Fragile. Concurrent writers competing on `refs/act/ops` have the same serialization problem as on main, plus the added complexity of cross-ref merges. Makes the multi-writer problem worse, not better.

**Behavior under merge/rebase:** Complex. The side ref diverges from main; syncing them is a novel problem. `git pull --rebase` on main doesn't pull the ops ref; agents have to know to pull two refs.

**UX impact:** High friction. Agents already struggle with worktree isolation; adding a second ref doubles the surface for confusion. **Not recommended.**

---

### Option 3: Squash-on-Close

**Concept:** When `act close` runs, it squashes all prior claim/create/update commits for that issue into a single "act: closed act-XXXX (claim → work → close)" commit.

**Implementation cost:** Medium-high. Requires tracking which commits belong to a given issue (parse git log for op markers), then `git rebase --autosquash` or equivalent (~250 lines, significant git complexity).

**Audit-trail preservation:** Partial loss. Individual commit timestamps for claim vs. close disappear from git log. Op files still have HLC so the data isn't gone, but the git audit trail flattens.

**Multi-writer correctness:** Dangerous. Squash rewrites history. In a multi-writer repo, rewriting shared commits will break other agents' rebases. **Not safe in the multi-writer scenario act is designed for.**

**Behavior under merge/rebase:** Unsafe. History rewriting + concurrent writers = force-push or conflict. Hard no for the architectural thesis.

**UX impact:** Cleaner log post-close, but the rewrite danger outweighs the benefit. **Not recommended.**

---

### Option 4: Compaction-Driven Cleanup

**Concept:** Similar to beads' "memory decay" feature — a periodic `act compact` command that summarizes old closed-issue ops into a single commit, pruning the per-op history.

**Implementation cost:** High. Needs a compaction algorithm that's safe across concurrent writers, produces a canonical summary, and doesn't break `act doctor`'s orphan-close detection.

**Audit-trail preservation:** Lossy by design. That's the point. Acceptable only if the per-op op files are also compacted (which defeats the audit-trail purpose).

**Multi-writer correctness:** Complex. Compaction is a global operation; two concurrent compactions would conflict. Needs a lock protocol.

**UX impact:** Invisible to daily use; occasional maintenance command. Lower mental model change than bundling.

**Assessment:** Worth having eventually, but a poor primary answer to the noise problem. Cleanup after the fact doesn't help during active work, which is when the noise is most visible.

---

### Option 5: Status Quo + Tooling

**Concept:** Keep one-commit-per-op. Add a `git log --invert-grep "^act-op:"` alias or `act log` subcommand that filters noise.

**Implementation cost:** Very low. One alias, one command, ~30 lines.

**Audit-trail preservation:** Full (nothing changes).

**Multi-writer correctness:** Full (nothing changes).

**UX impact:** Humans get a filtered view; agents are unaffected. But the underlying signal-to-noise ratio doesn't change — CI, GitHub UI, and most third-party git tooling will still show the noise.

**Assessment:** A useful complement to whatever else we do, but not a primary answer. The problem is at the production level where git log readers don't filter.

---

## Comparison to Beads/Doit Commit Pattern

**Beads architecture:** Dolt is a git-versioned SQL database. Every `bd create`, `bd claim`, `bd close` auto-commits to Dolt's internal history (one Dolt commit per write command). Standard git hooks (`pre-commit`, `post-merge`) batch those Dolt changes into normal git commits — multiple Dolt ops are bundled into one git commit when the agent does `git commit` for real work.

**Net effect:** A beads-managed repo shows no `bd-op:` noise in git log at all. Dolt is the op-log; git is the sync medium. The agent's work commit bundles whatever Dolt state changed since the last git commit. A typical "file → work → close" loop: **1 git commit** containing all issue state changes plus the code change.

**Direct comparison:**

| System | git commits per issue lifecycle | op-log granularity |
|---|---|---|
| act (current) | ~5 (3 act-op + 1 work + 1 merge) | per-op, in git |
| act (option 1: flush) | ~2 (1 bundled flush + 1 work) | per-op, in `.act/` files |
| beads | ~1 (work commit bundles Dolt state) | per-op, in Dolt |

**Beads' structural advantage:** Dolt is a proper database with its own version history; act uses git both as the op-log store and the sync mechanism. Option 1 (bundling) moves act closer to the beads model without requiring Dolt — the op files become the op-log, git becomes the sync medium.

**Is this comparison conclusive?** Mostly. The AGENT_INSTRUCTIONS.md for beads confirms "one Dolt commit per write command" and git hook batching. What's **not confirmed without running beads**: whether the pre-commit hook always fires correctly in multi-agent worktree setups, and whether Dolt's internal commit history is queryable for audit purposes in the same way act's op files are. **Recommended follow-up:** clone beads, run `bd init`, execute a `create → claim → work → close` loop, count git commits and `bd log` entries, compare to act's output on the same loop.

---

## Recommendation

**Implement Option 1 (bundling with periodic flush) behind a mode flag.**

Rationale: The per-op-commit design is not load-bearing — the op-log's audit trail lives in `.act/` files, not in git history. Bundling preserves every property of the multi-writer thesis while cutting git noise from ~3 act-op commits per issue to 1. This aligns with beads' effective design (Dolt as op-log, git as sync), adapted for act's simpler, git-native architecture.

**Mode split:**
- `dogfood/dev mode` (default when `ACT_MODE` unset or `dev`): keep current behavior (auto-commit per op). Visibility matters during development and dogfood.
- `production mode` (`ACT_MODE=production` or `--production` flag): defer commits, flush explicitly or on `act close`. One commit per closed issue at minimum.

A single flag is not the final answer — it's the test. If production mode sees adoption without complaints about lost audit trail, it becomes the default.

---

## Minimum Implementation Path

**Files to touch:**
1. `internal/cli/util.go` — `WriteOpAndAutoCommit` and `WriteOpsAndAutoCommit` live here; `WriteOpts.NoCommit bool` already exists. Add a `DeferCommit` mode that writes the op file but skips the `git add + commit` step, leaving the file staged-but-not-committed for a later flush.
2. `internal/config/config.go` — add `AutoCommit bool` field (default `true`); read from `ACT_MODE=production` env var or `--production` flag propagated from the root command.
3. `cmd/act/flush.go` — new `act flush` command; scans `.act/ops/` for files not yet committed (via `git status --porcelain`), stages them, produces a single `git commit` with enumerated ops in message.
4. `cmd/act/main.go` — wire `--production` as a persistent flag on the root command; inject into config before dispatch.

**Test coverage expected:**
- Unit: `WriteOp` in no-auto-commit mode leaves files unstaged; `CommitOp` stages and commits correctly.
- Integration: full `create → claim → close → flush` loop in production mode produces exactly 1 act-op commit; work commit with marker still required separately.
- Regression: existing tests that assert auto-commit behavior must pass in dev mode unchanged.

**Migration story for existing repos:** No migration needed. Op files are unchanged; only commit timing changes. Repos that upgrade act and set production mode will immediately get fewer commits. No schema change, no data migration.

---

## What's Still Uncertain

1. **Claim retry noise.** Retried claims (act-acd9, act-fdb2 each produced 2-3 claim commits) suggest the claim protocol generates more noise than the happy path. Does bundling help here, or do retries need their own fix?

2. **Flush trigger in agent loops.** If `act flush` is a new step agents must remember, it will drift from the canonical loop the way `git push` did before act-ac52. The flush should probably be automatic on `act close`, not a separate step. Need to decide the trigger before implementing.

3. **Beads verification.** The beads comparison is grounded in documented behavior but not empirically validated. A 30-minute local run of the `create → claim → close` loop in beads would either confirm or complicate the recommendation.

4. **Production-mode default timing.** When is the right moment to flip production mode to default? Probably not until the flag has been exercised in at least one non-dogfood repo — which means the alpha trial (act-evaluation.md) should explicitly test it.

---

## Post-implementation update — 2026-05-10 (act-728d shipped, then act-a659)

**What act-728d did:** implemented Option 1 as a `bundle_strategy` config knob with `per_op` (legacy) and `per_session` (new default). Under `per_session`, claim/close auto-commit and intermediate ops within the claim window defer to the close commit.

**What the post-shipping analysis found:** the typical lifecycle in this repo has _no_ intermediate ops between claim and close — agents claim, edit code, close. So `per_session` bundles 1 op (the close itself) into 1 commit. Net commit reduction: zero.

**Forward fix (act-a659, shipped 2026-05-10):** under `per_session`, `act close` writes + stages the close op, but **defers the commit when the working tree has uncommitted non-`.act/` changes**. The agent's next `git commit -am '<msg> (act-XXXX)'` subsumes the staged close op into the work commit. Typical lifecycle now: 2 commits (claim + work-with-close) instead of 3 (claim + work + standalone close). No-code closes (clean working tree outside `.act/`) still commit standalone, preserving single-command UX for tracking-only or wrong-claim closes. `--push` errors when the close stays staged because there's nothing on HEAD yet to publish.

**Naming note:** the original design draft proposed `BEADS_MODE` as the env var; `ACT_MODE` is the correct name for this project. The bundle_strategy field shipped with `per_op`/`per_session` rather than dev/production naming because the semantics are about op-vs-session granularity, not environment.

**What's still uncertain:**
- Whether to deprecate `per_op` once `per_session`+act-a659 has been exercised in another repo — keeping both is a small ongoing cost (test matrix, mental overhead) for an escape-hatch nobody may need.
- Whether the canonical loop in CLAUDE.md should _require_ the close-then-commit order or accept commit-then-close as a 3-commit fallback for agents who haven't internalized the new ordering.
