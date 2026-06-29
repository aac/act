# Coordination Plane Design — act-f048

_2026-05-17. Design note for the coordination-plane reframe. Supersedes the multi-env research issue (act-8d67). Subsumes the deferred quiet-the-op-log brainstorm and the residual op-log-noise Phase-2 territory. v2 reflected findings from the three-reviewer pass on commit 73ff71c. v2.1 (this version) drops backward-compat machinery per the maintainer's explicit guidance — fewer than 10 act-using projects exist, all the maintainer's, all on one machine, so config knobs / gating flags / vestigial fields written to ease migration for a public userbase aren't load-bearing here. See "Review trail" at the end._

## Reframe

**Act is contributor infrastructure, not project history.**

Today, `.act/` lives inside the code repo and its op-log is committed to the same branch as code. Every claim/close/create produces an `act-op:` commit on `main`. In active repos this means ~70% of `git log` is act bookkeeping (the shipped `per_session` bundling work (act-728d / act-a659) reduced lifecycle commits modestly but did not change the order-of-magnitude shape). The problem is architectural, not tactical: tactical fixes (bundling, side-refs, log filters) all leave act state inside the public history.

The right move is structural: act state lives outside the host project's shared git history. Specifically, `.act/` becomes a nested git repository, gitignored from the surrounding code repo. Work commits on the code repo's main branch continue to carry `(act-XXXX)`-style markers — those markers are the only act-shaped artifact that survives in the public history. Everything else (ops, claims, dependency edges, op-log history) lives in the nested act repo.

Two corollaries fall out:

1. **Code is canonical for the closed-work slice; act is sole source for everything else.** Work commits with markers are the durable record of *closed work that produced code*. Active backlog, dep edges, claims, priorities, HLC history, tracking-only issues, no-code closes — none of these have a code counterpart and never will. `act doctor` becomes a reconciler that cross-references the closed-work slice between code and act; for the rest, act is authoritative and the nested act repo's history is the audit trail. (v1 of this doc overclaimed "code is canonical, act is overlay" — the framing is true only for the closed-with-marker subset, see Architecture review finding #7.)
2. **Scope is operator-decided, not project-bound.** The boundary "agents that should see each other's claims" sometimes maps to one code repo, sometimes to several, sometimes to a subset of one. The operator picks the boundary when they `act init` or hand state to an agent in its prompt. Pairing with a code repo is convention, not architecture. **For Phase 1, the convention is "nested `.act/` under the host repo root" — the operator-decided broadening is preserved as Phase 2 design space.**

The reframe also achieves the OSS-adoption property that's been blocking public release of ask/act/poke: a contributor cloning the public repo gets exactly the code, with no act state visible (subject to the defense-in-depth caveats in the "Public-repo concerns" section below).

## Two-phase shippability

The full reframe naturally splits into two independently-shippable phases. Phase 1 is on the critical path for public release. Phase 2 unblocks the distributed-agent case but is not required for public release.

### Phase 1 — local-only

`.act/` is a nested git repo, gitignored from the surrounding code repo. State lives entirely on one machine. Concurrent agents in the same working tree continue to coordinate via act's existing atomic-op semantics. The host project's `git log` becomes pristine of act-ops.

Phase 1 is sufficient for any project that runs all its agents in one place — including every project on the current public-release path. It does *not* address the case where one contributor's agents run on multiple machines (laptop + Claude Code on web + dev sandbox + CI) and need to see each other's claims; that's Phase 2.

**Acknowledged regression during the Phase-1/Phase-2 gap:** the maintainer's own multi-machine workflow stops sharing act state across machines until Phase 2 ships. This is accepted with eyes open — the OSS-unblock value of Phase 1 outweighs the maintainer-side temporary regression. To minimize the cost: ship Phase 2 promptly after Phase 1 exercises clean in this repo.

**Why Phase 1 is dramatically simpler than the full design:** with no cross-machine sync, most of the reconciliation territory dissolves. There's no sync-failure semantics (nothing syncs). There's no multi-environment doctor. Doctor's only job is the local cross-check: walk the code repo's `git log` for markers, walk the local act state, report anomalies.

### Phase 2 — contributor-replicated

A contributor can configure an act remote (default off). When configured, their agents on any machine sync state against that remote via `act sync` (implicitly on `act close --push`). Cross-machine coordination uses `git push/pull/merge` on the nested act repo's history. The remote is private by default and owned by the contributor; the code repo's remote knows nothing about it.

Phase 2 is where the harder design questions live (sync failure semantics, two-remote push fanout, marker collision across multiple act states). Phase 1 doesn't depend on any of them being resolved.

## Invariants

These hold under both phases:

- **No act state in shared project history.** No `.act/ops/*` files committed to the host repo, no `act-op:` commits in the host repo's history going forward. The host project's `.gitignore` includes `.act/`. This is the load-bearing property; the design exists to enforce it structurally, with defense-in-depth (see "Public-repo concerns").
- **Work-commit marker convention survives.** Work commits authored by agents continue to include a marker. Placement is trailer form (`Act-Id: act-XXXX` in the commit body) — invisible to conventional-commit linters, preserved by squash-merge, ignored by semantic-release CHANGELOG generators, and easy for external contributors to ignore. Doctor's grep matches both the trailer form and the historical subject-line `(act-XXXX)` form so existing repos' pre-migration history resolves cleanly. No config knob; trailer is the only emission shape going forward.
- **Atomic op semantics preserved within each repo.** Each op file write + commit is atomic within the nested act repo, same as today. Cross-repo atomicity (close op in nested + work commit in host) is a new concern with an explicit contract; see "Atomicity under Phase 1" below.
- **Multi-agent concurrent claim merging continues to work** within a single act state. Two agents claiming simultaneously in the same `.act/` resolve via the same conflict-free mechanism today.
- **CLI surface mostly preserved.** `act ready`, `act update --claim`, `act close --reason`, etc. work unchanged. Phase 1 changes which git repo write paths target; the user-facing verbs don't shift. `act close` gains an optional `--no-code` flag so legitimate no-code closes can be distinguished from "agent closed but never pushed code" (see "Doctor reconciliation").
- **Audit trail per-act-state.** The nested act repo's own history (claim sequences, who-closed-what, HLC) is fully queryable by `act log`, `act show`, `act doctor`, etc. — it just lives in the nested repo, not the code repo.
- **No mandated cross-contributor visibility.** Default scope is "the agents of one operator." Multi-contributor coordination is possible via a shared act remote (Phase 2) but is not the default.

## Doctor reconciliation

The shape of the reconciliation contract is the central design point that's *not* what v1 of this doc implied. v1 enumerated five edge cases as load-bearing; this revision narrows to the actually-load-bearing shape.

| Case | Severity | Behavior |
|---|---|---|
| (a) marker in code, no matching issue in act state | Real but rare under Phase 1 (only via hand-edit, wiped `.act/`, or migration historical-id residue); common under Phase 2 (cross-machine desync) | Report as warning. Under Phase 2, suggest `act sync`. Under Phase 1, suggest investigation. Doctor `--strict` promotes to error. |
| (b) issue in act state, no closing marker in code | Normal *only when* the issue is `type=tracking` or the close op carries an explicit `no_code=true` reason. Otherwise it's the "agent closed but never pushed code" silent-desync. | Ignore when no-code marker present on close. Otherwise warn. (Correctness review finding #2.) |
| (c) multiple markers on different commits for same id | Normal (multi-commit work, fix-up commits) | Ignore |
| (d) marker referencing unknown id | Rare (typo, deleted issue, cross-state reference) | Warn; agent investigates. For external PRs (commit author not in `git shortlog` of internal contributors, or merge commit from a fork), suppress as expected — see "Public-repo concerns / fork-PR flow." |
| (e) claims newer than most recent code commit | Normal (work in progress) | Ignore |

**Contract summary:** doctor lists markers in the code log, cross-references against act state, warns on (a) and (d) (with the suppression heuristics above), distinguishes legitimate (b) from silent-desync (b), and ignores (c)/(e). `--strict` promotes warnings to errors. Doctor is liberal in what it accepts; the canonical agent loop runs doctor in the review step at non-strict severity, while CI can run `--strict` to catch real regressions.

The previous draft's five-edge-case "reconciliation contract" turns out not to need a deterministic spec — it's a small piece of doctor code with a handful of heuristics. We don't need a worked spec before implementing.

## Atomicity under Phase 1

Each write op was previously a single atomic write-and-commit in the host repo. Under Phase 1 the close op specifically becomes a two-repo affair: close op in the nested act repo, work commit in the host repo. The design needs an explicit contract for the in-between state.

**Write order.** Nested commit first (close op), then work commit in host repo (with marker). Rationale: a code commit without a corresponding close op is the existing-and-tolerated (b) case (work landed, tracking incomplete); a close op without a code commit is harder to recover (act says "this is done" but the code doesn't reflect it). Better to leave the cheap-to-recover state at the boundary.

**Recovery contract.**
- Nested commit fails: abort before code commit. Caller sees the close-op failure and retries.
- Nested commit succeeds, code commit fails: doctor's next reconcile observes case (b) without a no-code marker, warns. Agent prompted to either re-run the work commit or, if work was lost, run `act update` to roll the issue back to in-progress.
- Both succeed but `git push` of code repo fails: existing failure mode, unchanged. Re-run `git push`.

**Consequence for act-a659 (close-stages-into-work-commit).** The shipped mechanism cannot work across two repos: you cannot stage a blob in repo A and commit it via repo B's index. Under Phase 1, `bundle_strategy=per_session` **collapses to per-op behavior** — every close produces a standalone commit in the nested act repo. The noise that motivated act-a659 was *visibility in the host repo's log*; under Phase 1 the close commit is in the nested repo and invisible to the host, so the bundling optimization is unnecessary. (Architecture review finding #1, Correctness review finding #4.)

The CloseResult JSON's `staged_for_commit: true` field becomes always-false under Phase 1; agents don't need a follow-up `git commit -am` to subsume the close. The canonical loop simplifies: `act close` is again single-command and produces its own commit in the nested repo, while the agent's separate `git commit -am '<msg> (act-XXXX)'` in the host repo is independent.

## Implementation delta

The delta from today's act, by phase. Each item below is a candidate implementation issue.

### Phase 1
1. **`act init` two-repo bootstrap.** `git init` inside `.act/`; commit the new state (initial config, schema, empty op-log) to the nested repo. Separately, append `.act/` to the host repo's `.gitignore` and commit that change to the host. No flag-gated rollout — nested is what `act init` does. Spec the failure modes: nested-init succeeds + host-gitignore fails, vice versa, and the gitignore-edge-cases (entry already present, present-without-newline, missing file, ignored-at-different-scope). The `CommitResult` envelope grows a per-side commit shape.
2. **Gitops dual-handle.** Today `internal/gitops/gitops.go` is single-`RepoRoot`. Phase 1 needs two handles: `actGitOps` (writes ops, queries nested-repo history) and `hostGitOps` (scans `(act-XXXX)` markers in host commit log, no write access from act commands). Every call site that today uses `gops` picks the right one. This is the bulk of the implementation work; "write-path retarget" in v1 understated it.
3. **Host-vs-nested repo-root resolution.** Today `findRepoRoot` walks up to `.git`; under Phase 1 the nearest `.git` may be the nested act repo's. Resolver needs to find the *host* repo root (skip-nested-act-repo logic) and the act state path (the nested `.git` directory's parent) distinctly. (Architecture review finding #2/4.)
4. **Hook cwd convention.** `.act/hooks/close` currently runs `go test ./...` etc. from the host repo root. Pin the convention: hooks always run with `cwd=host-repo-root`, with `$ACT_STATE_PATH` set to the nested `.act/` directory. Document in the hooks contract.
5. **Doctor reconcile-lite.** Implement the table from "Doctor reconciliation" above. Doctor takes both gitops handles; greps the host repo log for markers, walks the nested act state, reports the (a)/(b)/(d) anomalies per the table. Add `--strict` to promote warnings to errors. Add the doctor-side gitignore-actually-effective check (run `git check-ignore .act/` against the host repo) as a sanity probe.
6. **Migration: initial-state import.** Promoting Open Question #3 to a specified decision: option (a), import existing `.act/` op files as the nested repo's initial commit (one bulk commit per migrating repo). Rationale: keeps the pre-migration id-space reachable from the nested repo, eliminating false-positive (a) warnings for pre-migration markers (Correctness review finding #1, Architecture finding #5). Historical op commits in the host repo's log stay there as the "before" record but become read-only history; new ops never join.
7. **Gitignore defense-in-depth.** A `.git/hooks/pre-commit` hook installed by `act init` in the host repo that rejects any staged addition/modification under `.act/`. Staged deletions of `.act/*` are permitted so a manual `git rm -r --cached .act/` (the untrack shape used to un-track a `.act/` that an older host repo still tracks) succeeds under a normal `git commit` even on a branch where the hook is already installed. Doctor surfaces it if the hook is missing or `.act/` becomes accidentally tracked. The remedy recipe (`git rm -r --cached .act/`) is in the doctor error message. (OSS review finding #1.)
8. **CI-friendly no-state behavior.** Any act command run in a directory without `.act/` exits 0 with a one-line "no act state here" message, not error. Audit this repo's CI for `act` invocations and convert to guarded form. (OSS review finding #2.)

### Phase 2 (later)
9. **`act sync`** — push/pull the nested act repo against its configured remote. Bundle into `act close --push` and similar verbs when remote is configured.
10. **Remote configuration commands** — `act remote add/remove`, persisted in the nested act repo's `.git/config`.
11. **Doctor cross-repo** — take one or more code-repo paths as input.
12. **Sync-failure semantics spec.**

Phase 1 is closer to **6-8 issues** than v1's "3-5 issues" estimate. Phase 2 is sized when we get there. Everything else — op file format, HLC, claim protocol, idempotency, the canonical loop's shape — stays.

## Public-repo concerns

Phase 1's claim is "outside contributors see exactly the code." That's not automatic; it requires several decisions to be made explicitly.

### Marker placement

Old subject-line `(act-XXXX)` markers collide with common OSS workflows: squash-merge collapses N markers into ≤1, conventional-commit linters (commitlint, husky) reject trailing parentheticals, semantic-release surfaces them in generated CHANGELOG entries, and external contributors can't resolve them. (OSS review findings #3, #4.)

**Decision.** Trailer form (`Act-Id: act-XXXX` in the commit body) is the only emission shape going forward — no config knob, no flag, no per-repo opt-in. Trailer form is invisible to conventional-commit linters, preserved cleanly by squash-merge, ignored by semantic-release, and easy for external contributors to ignore. Doctor's marker grep matches both the new trailer form and the historical subject-line form so pre-migration markers in existing repos still resolve, but new markers are always trailers.

### Fork/PR flow

External contributors forking a public act-using repo will not have `.act/` (it's gitignored on the upstream). Their `act init` (if they run it) creates a fresh, independent act state. When they submit a PR, they should not be expected to add markers — the maintainer adds a marker on merge if the PR is tracking-relevant. (OSS review finding #5.)

**Doctor heuristic to suppress case (d) for external PRs:** commit's author or co-author is not in the project's `internal_contributors` config (or, simpler default: commit is a merge commit and its parents include a commit not authored by anyone in `git shortlog -sn` of the last N commits). The suppression is opt-out; doctor logs it as "suppressed: looks like external PR" so the maintainer can audit if needed.

### Gitignore as sole enforcement is fragile

Beyond the defense-in-depth pre-commit hook (item 7 in the delta), document the failure modes explicitly: rebase can drop the gitignore line; `git add -f .act/` bypasses; a tracked-then-gitignored `.act/` needs `git rm --cached`. The `act doctor` gitignore-effective check is the runtime safety net.

### CONTRIBUTING template

`act init` emits a short stanza into CONTRIBUTING.md (or appends to an existing one) for any host repo with a public-looking remote: "Maintainers use a tracker that adds an `Act-Id:` trailer to commit messages. External contributors don't need to do anything with these — submit PRs normally and we'll add trailers on merge if relevant." This makes the convention discoverable without requiring contributor participation. (OSS review finding #7.)

## Migration story

- **This repo (act).** One-shot migration; no flag-gated rollout (the entire act-using population is the maintainer's ~10 projects on one machine — no audience to ease into the change). Run the conversion in one transaction: `git init .act/`, import existing op files as the nested repo's initial commit (per delta item 6), append `.act/` to host `.gitignore`, `git rm -r --cached .act/` to un-track existing tracked op files. From the next commit on, all act ops live in the nested repo and the host log is pristine. Historical op commits in the host log stay as the "before" record but are dead history.
- **Downstream repos** (the maintainer's other act-using repos). Same one-time conversion. Phase 1 only. No remote, single-machine.
- **Already-public-history act-op commits in any repo.** Leave them. The point of the reframe is forward-going pristine history; historical noise is not worth rewriting history to fix.
- **The deferred quiet-the-op-log brainstorm** is closed by this design.

## Nested-git pain points (acknowledged, mitigated)

Real interactions to call out so the migration doesn't surprise anyone (Architecture review finding #6):

- **IDE source-control views.** VSCode and JetBrains detect nested `.git/` and may show competing source-control panels. Workaround: tell users to ignore the act-repo SCM view, or add an editor-specific `files.watcherExclude` for `.act/`. Document for the repos we migrate.
- **`git clean -fdx`** in the host repo will blow away the entire nested `.act/` (it respects `.gitignore` by ignoring tracked files, but `-x` adds gitignored files back into scope). Document the recovery path: if you have an act remote (Phase 2), re-clone the nested repo; under Phase 1, restore from filesystem backup or re-import from a sibling clone.
- **Worktrees.** This repo's CLAUDE.md mandates `isolation: worktree` for sub-agents. Under Phase 1, a worktree of the host repo does *not* share the nested `.act/` (it's outside the host's tracked files). This is probably what we want — sub-agents have separate working trees and separate claim state — but verify the agent loop still composes. If sub-agents need to share claims with the parent, they need an explicit `--act-state-path` pointing back to the parent's `.act/`.
- **`rg` / `grep`** default-respect `.gitignore`, so search tools will skip `.act/` when running from the host. For most agent work this is correct; for debugging act itself, use `rg --no-ignore .act/`.
- **Submodule confusion.** Users will sometimes assume `.act/` should be a submodule. It isn't. Document.

## What is NOT changing

- The canonical work loop's shape (`ready → claim → work → close → push`). The push at step 7 still pushes the code repo's branch. Phase 2 adds an implicit `act sync` alongside.
- Op file format, HLC, claim protocol, idempotency guarantees.
- The work-commit marker convention. The *placement* becomes configurable (subject vs trailer); the *fact* of a marker is unchanged and load-bearing for doctor.
- Phase 1 of the four-phase orchestration plan (agent-push-to-main). Orthogonal.
- `bundle_strategy` config knob is removed entirely. It was a hedge for host-repo log noise that no longer exists. If a future bundling concern emerges for the nested repo's own history, file fresh — the constraint shape will be different.

## Open questions

The genuinely-open questions after this revision. Several previous questions are now resolved (reconciliation contract → see Doctor section; migration import → option (a) in delta item 6; contributor scope → operator-decided, documented; marker placement → trailer-only, no config knob, see Public-repo concerns / Marker placement). What's left:

1. **ID width / collision risk.** Current short id is 4 hex chars (~65k space). Doctor's marker grep operates at this width. Once Phase 1 ships nested-repo-per-project, contributors will run several act states; birthday-collision math says collisions appear within a few hundred issues per state. Widening to 6 hex (~16M space) before lock-in is cheap. (Correctness review finding #6.) **Recommended action: widen short id to 6 hex chars in Phase 1, before the migration cost compounds.** Filed as `act-f9a0`.
2. **Doctor's `--strict` integration with the canonical loop.** When does an agent run doctor at all under Phase 1, and at what severity? Probably: non-strict in the review step, strict in the host repo's CI. But the canonical loop in CLAUDE.md doesn't currently mandate doctor at all. Decide whether Phase 1 adds a doctor invocation to the canonical loop. (Correctness review finding #5.)
3. **Worktree-sub-agent claim sharing.** Per the "Nested-git pain points" section, sub-agents in worktrees get separate `.act/` by default. Confirm via dogfood that this is desired; if claim-sharing is needed, decide whether `--act-state-path` is the right plumbing or something more implicit.
4. **Phase 2 sync-failure semantics.** Deferred to Phase 2 design pass.

## Relationship to other issues

- **act-8d67** (research: separate-repo or separate-remote model): supersedes. Close as superseded when this design lands.
- **act-6c73** (pure-metadata bundling for inbox-triage): largely dissolves under Phase 1. Re-evaluate scope; probably close as obsoleted.
- **act-6018** (commit noise design note): Phase-2 territory becomes obviated.
- **act-208e** (orchestrator-scoped bundle_strategy): re-evaluate. The bundling pressure largely evaporates.
- **act-dfa5** (noise-related): re-evaluate.
- **act-2c7d** (act init auto-commits .act/ + .gitignore by default): tightly coupled to Phase 1 implementation issue #1; close or fold into the new issue.
- **The deferred quiet-the-op-log brainstorm memory**: closed by this design.

## Acceptance for act-f048

1. ✓ Design note in repo (this file).
2. ✓ Reframe + reconciliation behavior documented per the trimmed shape above.
3. ✓ External review on this design (three reviewers, commit 73ff71c; findings folded into v2 as documented in "Review trail" below).
4. Phase 1 implementation issues filed per the 8-item delta. (Pending.)
5. act-8d67 closed as superseded; act-6c73, act-208e, act-dfa5, act-2c7d re-evaluated. (Pending.)
6. Phase 1 migration exercised in this repo before Phase 2 begins. (Pending — gated on Phase 1 implementation issues landing.)
7. Phase 2 deferred to a separate design pass. (Done — explicitly out of scope here.)

## Review trail

v1 (`73ff71c`) was reviewed by three parallel agents under three lenses: architectural soundness, OSS-adoption friendliness, correctness/reconciliation. v2 folded the load-bearing findings:

- **Load-bearing structural fixes:** act-a659 cross-repo bundling impossibility → `per_session` collapses to `per_op` under Phase 1 ("Atomicity under Phase 1"). Implementation-delta expansion from 4 to 8 items. Migration import promoted from Open Question to specified decision (delta item 6).
- **Doctrine refinements:** case (b) narrowed to "ignore iff tracking-only or no-code" ("Doctor reconciliation" table). "Code is canonical" framing narrowed to closed-work slice. Operator-decided scope explicitly deferred to Phase 2.
- **New design surface added:** atomicity contract. Public-repo concerns: marker placement, fork/PR flow, gitignore defense-in-depth, CONTRIBUTING template. Nested-git pain points. CI-friendly no-state behavior.

v2.1 (this revision) drops backward-compat machinery per the maintainer's explicit guidance (fewer than 10 projects using act, all the maintainer's, on one machine — optimize for the future rather than backward compatibility):

- `marker_placement: subject | trailer` config knob removed — trailer is the only emission shape; doctor's grep still matches both forms so historical markers resolve, but there's no per-repo config to manage.
- `act init --nested` flag removed — nested-repo bootstrap is what `act init` does.
- `bundle_strategy` config field removed entirely — was a host-log-noise hedge that the reframe dissolves; if a nested-repo bundling concern emerges later, file fresh.
- Migration story simplified from "ship behind flag, dogfood for a week, flip default" to "one-shot, no flag." The maintainer runs the conversion on ~10 projects under their control; there's no audience to ease into the change.

These simplifications shrink the implementation surface (notably act-b382, which was a config-knob-plus-flag implementation, becomes a smaller "switch emission to trailer form + extend doctor regex" change) but don't change the structural decisions from v2.
- **New Phase-1 sub-question raised:** ID width widening before lock-in (Open Question #1).

Two recommendations from the OSS review were considered and not adopted as defaults in this revision: (a) making `trailer` the marker placement default for all new `act init` rather than `--public` opt-in (deferred to Open Question #2 pending external-contributor friction data); (b) blocking-by-default doctor on case (a) anomalies (kept as warn-only with `--strict` opt-in to match the existing `checkOrphanClose` convention; agent-loop integration is Open Question #3).

A few low-priority items from the reviews are deliberately deferred to the Phase 1 implementation issues rather than re-spec'd here: gitignore edge-case enumeration (Correctness #7) belongs in the `act init` implementation issue; act-init bifurcated commit envelope (Architecture #3) belongs in the same implementation issue.

---

# Phase 2 design — contributor-replicated coordination plane

_Originally a separate brief (v4, plan-ready; drafted 2026-05-18, two review gates plus one remediation pass). Merged here so the coordination-plane rationale lives in one doc. Builds on the Phase 1 design above (as-built in `f3d9945`..`3298840`) plus the Phase 1.5 worker-isolation pivot (umbrella `act-b77a80`). Phase 1's "Phase 2 deferred to a separate pass" is closed by this section._

## What Phase 1 + 1.5 doesn't support (the Phase 2 problem)

Phase 1 + 1.5 ships a usable plane for single-machine dogfood (≤4 concurrent agents per pass, harvest at teardown). It does not support: (1) **cross-worker visibility during execution** — a worker's mid-flight ticket isn't visible to peers until both tear down and harvest; (2) **multi-machine work** — the nested `.act/.git` has no remote, so state is captive to the orchestrator's machine; (3) **sandboxed workers without filesystem share** — brain/hands containers and remote VMs can't participate without out-of-band copy.

Phase 2 closes all three with one mechanism: each worker gets its own clone of the coordination plane, pushes ops as it writes them, and fetches peers' ops on read. Harvest stays but its scope shrinks (see "Harvest in Phase 2").

## Trust model and target

**Target:** one operator coordinating one or more agents across one or more machines under their control — the widest scope Phase 2 plans for. Explicitly out of scope: team collaboration (multiple humans), multi-tenant remotes, adversarial/untrusted writers.

**Auth:** none beyond filesystem and SSH. Workers reach the orchestrator's `.act/.git` over local path (same machine) or `ssh://` (remote). Whatever credentials git already has suffice; there is no project-level auth, ACL, or signing. Acceptable because every participant is operator-controlled.

**Op-log sensitivity:** ticket descriptions can carry code references, paths, occasional pasted credentials — so the op log is as sensitive as the source tree, and anything that backs up the tree should back up `.act/.git`. The optional GitHub upstream (see Topology) must be private; doctor warns if it's configured public.

**Inherited load-bearing properties from Phase 1:** HLC + nonce filenames mean two writers never collide on op-file paths, so set-union merge of two `ops/` dirs is exact. Append-only ops mean no edits or deletes and no edit-conflicts — the only mutable thing in `.act/.git` is the branch ref, which fast-forwards trivially under HLC ordering. Deterministic fold with HLC LWW resolves semantic conflicts (two workers claiming one issue) at fold time without coordination; the loser gets `claim_lost` on next read. The `Act-Id:` trailer contract is unchanged.

## Topology — orchestrator's `.act/.git` is canonical

One git repo for the plane: `<project>/.act/.git` on the orchestrator. Workers clone from it and push back; no separate bare repo. The orchestrator sets `receive.denyCurrentBranch=updateInstead`, which permits pushes to the checked-out branch and auto-updates the working tree iff it's clean (the dirty window is sub-second, between `act` staging an op file and committing it).

Workers clone `<orchestrator>/.act/.git` (absolute path for same-machine, `ssh://` for remote — standard git URLs, no new transport).

**Optional GitHub upstream** (`origin-upstream`) for durability: replication is by explicit `act remote sync`, **not** a post-commit hook on worker clones (fires mid-rebase during contention, publishing partial state) and **not** cron (multi-minute durability gap). Workers never talk to the upstream — only to the orchestrator's `.act/.git`. Recovery if the orchestrator machine is gone: clone the upstream, reconfigure as the new orchestrator.

**`act remote sync` fires from two points, both on the orchestrator:** (1) a **post-receive hook** on `.act/.git` (installed by `act remote enable`) that detaches and runs sync after a worker push lands a ref — never mid-transfer, never mid-rebase; (2) the **`actGitOps` write path** when the orchestrator runs its own `act` command, triggering a background sync after its commit. Both spawn sync detached and return immediately. Sync is **fail-soft**: a failed upstream push logs to `.act/.sync-log`, and the next successful sync clears the queue. Silent degradation eventually surfaces via doctor case (h) below (upstream-behind-local threshold check), not inline.

## Sync protocol — eager writes, cached reads

Writes push synchronously; reads fetch with a TTL cache; specific commands bypass the cache.

**Write path:** commit to the local clone, then `git push origin <branch>`; the write doesn't return success until push lands. Failure modes: network unreachable → `remote_unreachable` (suggest `--offline`, which queues the op locally and retries on next reachable write); push rejected after exhausted retries → `push_exhausted` (distinct from transient); local commit succeeded but push failed → op exists locally, recovered via the harvest fallback.

**Read path:** `act show`/`ready`/`log` reads local state if `.act/.git/FETCH_HEAD` mtime is within the TTL window, else `git fetch && git rebase origin/<branch>` first. **Post-rebase invariant:** `.act/fold-checkpoint.json` and `.act/index.db` MUST be treated as stale; the next fold re-verifies against the current tree hash. TTL is `act.readCacheTTLSeconds` (default 5) in `.act/.git/config`; a local write since the last fetch also counts as fresh.

**Bypass the cache (always fetch):** `act doctor`; `act ready` in orchestrator dispatch mode (the orchestrator sets `ACT_DISPATCH_MODE=1`, since a stale ready here causes duplicate-claim work); anything passed `--fresh` / `--no-cache`.

## Push contention — fetch, rebase, push, verify

Two workers commit ops with disjoint filenames (HLC+nonce) and both push; the second gets non-fast-forward. Retry loop: (1) `git fetch origin <branch>`; (2) `git rebase origin/<branch>` — zero conflicts because each commit only adds disjoint new files; (3) `git push`; (4) **post-push reachability verification** — fetch again and `git merge-base --is-ancestor <local-HEAD> origin/<branch>`; if not reachable the push was silently rejected, go to (2); (5) after N (default 5) retries with backoff capped at 1s, abort with `push_exhausted`.

**Why rebase, not merge:** linear op-log history — the ops carry HLC, so merge topology adds only noise and `merge --ff-only` would always fail after the first concurrent push.

**Why the reachability check is mandatory:** `git push` can exit 0 without the ref advancing in two cases — a network drop between object transfer and ref-update, or `updateInstead` silently rejecting a push that arrives while the orchestrator's tree is dirty. Without the check, the worker believes its ops are published when they aren't. `updateInstead` doubles as the cross-write serialization lock: it refuses pushes while the tree is dirty; orchestrator local writes don't push (already on canonical), so they only need to commit fast enough that the dirty window stays narrow.

**Slow-write warning (write-time, not doctor-time):** `actGitOps` measures wall-clock between op-file stage and commit; over `act.slowWriteThresholdMs` (default 1000ms) it warns to stderr and appends to `.act/.slow-writes` (capped at 100 entries). Doctor's remote-status block surfaces a recent count — it cannot retroactively time past writes. A persistently slow filesystem (NFS, FUSE, encrypted overlay) becomes visible this way.

## Per-worker clone lifecycle

Replaces Phase 1.5 copy-on-dispatch for remote-attached workers. **Dispatch:** `act bootstrap-worker --from-remote <orchestrator-url> <worktree>` clones `--depth 1` (fold replays op files, not git history, so deep history is dead weight), into a temp dir atomically renamed on success (no debris on a stalled clone), honoring `act.bootstrapTimeoutSeconds` (default 30s), validated by a round-trip `act ready`. **Shallow-rebase fallback:** if a contention rebase hits a shallow-depth error and the server won't auto-deepen, the loop runs `git fetch --unshallow` once, then operates on full history — `--depth 1` is best-effort fast path, full clone the fallback. **Execution:** the worker calls `act` normally and never knows it's a worker. **Failure:** if the dying worker's last write pushed, no recovery needed; if a local commit hadn't pushed, harvest it before teardown. **Teardown:** the clone is discarded; all confirmed-pushed state is already on the orchestrator.

**Sandboxed-no-network workers** keep the Phase 1.5 path: copy `.act/` in via `act bootstrap-worker` (no remote), write locally, harvest at teardown.

## Doctor in the three-state world

Phase 2 adds the remote as a third state alongside code and local act. Phase 1's five cases mostly gain a remote sub-case:

| Case | Phase 2 behavior |
|---|---|
| (a') marker in code, none local, remote has it | Fetch and converge — the common case during a worker's execution. |
| (c') local and remote disagree on status | HLC LWW picks the winner; doctor logs the resolution. |
| (f) issue exists locally, never pushed | Warn (exit 0); suggest manual push or harvest. Detect: `git diff-tree origin/<branch>..HEAD -- .act/ops/` non-empty after a fresh fetch. |
| (g) remote ahead but fetch failed | Error (exit 4, `remote_unreachable`) unless `--no-fetch` downgrades to warn. Detect: fetch non-zero or hangs past `act.fetchTimeoutSeconds` (default 10s). |
| (h) optional upstream behind orchestrator | Warn upstream-drift; suggest `act remote sync`. Detect: `git rev-list --count origin-upstream/<branch>..origin/<branch>` over `act.upstreamDriftThresholdCommits` (default 50) or `...Seconds` (default 3600), whichever first. Only fires if `origin-upstream` is configured. |

Doctor fetches before checks (unless `--no-fetch`) and includes a remote-status block.

**Clock skew / op-log growth.** HLC tolerates skew up to the inter-participant clock delta plus the logical-counter range — fine as long as no clock is off by more than a few minutes; doctor warns on any op dated > now + 10min (mis-set clock or corrupt write). The op log is append-only with no Phase 2 compaction: ~500 bytes/op, ~18MB/year at heavy single-user load; if it ever crosses a soft threshold, `git gc --aggressive` on `.act/.git` is the standard answer.

## Migration from Phase 1 + 1.5

Preconditions: nested layout already in place (`act init` produces it) and the orchestrator machine identified. Steps: (1) `act remote enable` on the orchestrator — sets `receive.denyCurrentBranch=updateInstead` and the `act.*` config defaults, runs a doctor pass; (2) optionally `act remote add-upstream <github-url>`; (3) move dispatch to clone-based bootstrap (`act bootstrap-worker --from-remote ...`); (4) revert with `act remote disable`. All ops keep folding correctly — Phase 2 doesn't change the op format. This also resolves the worktree regression where `git worktree add` didn't carry `.act/`: every worktree now gets its own clone.

## Harvest in Phase 2

Harvest is scoped, not retired. **In scope:** sandboxed-no-network workers (copy-on-dispatch + harvest-on-teardown) and crash recovery (a worker that local-committed but never pushed). **Out of scope:** normal teardown of remote-attached workers (they pushed during execution; harvest no-ops). Harvest is always **orchestrator-initiated** — a no-network worker can't fetch to diff itself, so the orchestrator inspects the worker's `.act/ops/` directly, diffs against its own set, and copies only the delta. **Idempotent (test-enforced):** harvesting already-landed ops is a no-op; the HLC+nonce filename guarantee makes the op-set diff exact. `--push-every-op` forces immediate push per write for networked sandboxed workers where harvest isn't practical (off by default).

## Phase 1.5 → Phase 2 cutover

Phase 1.5 runs the dispatcher on copy-based bootstrap: the orchestrator seeds each worker's `.act/` with `act state import <path>` and collects new ops with `act state export <path>` at teardown. Phase 2 keeps that path as a fallback but adds a push-attached channel — workers push ops directly to the orchestrator's `.act/.git` during execution and the orchestrator replicates to a configured upstream.

**One-time per-project setup** (on the orchestrator's checkout, once): `act remote enable` writes `act.role=orchestrator` into `.act/.git/config`, sets `receive.denyCurrentBranch=updateInstead`, and installs the post-receive hook that invokes `act remote sync` on every push. Optionally `act remote add-upstream <url>` wires the upstream the hook syncs to; it refuses public URLs unless `--force-public` (see `docs/spec.md` for the `upstream_public` error code).

**Cutover order:** (1) enable on the orchestrator first — existing Phase 1.5 workers keep operating unchanged, landing commits via `act state export` at teardown; (2) new dispatches use `act state import --from-remote <url> <path>`, where `<url>` is the orchestrator's `.act/.git`; (3) mixed waves are safe — copy-source and push-attached workers coexist and the orchestrator's fold converges either way.

**Rollback:** `act remote disable` removes the post-receive hook file and unsets `act.role`; new workers fall back to copy-source bootstrap automatically, and in-flight Phase 2 workers fail their next push with a clean error, recoverable via the fallback `act state export` path. `act remote disable` is idempotent — running it twice exits zero both times.

## Spec and test-discipline dependencies

Phase 2 adds two caller-visible error codes — `push_exhausted` (retry-limit exhaustion) and `remote_unreachable` (transient network failure). Per the repo's doc-discipline rule, the same commits that introduce them must: enumerate them in the error-envelope table in `docs/spec.md` with exit-code mappings (both exit 4, distinguished by code + `details`); add `Envelope.Code` constants in `internal/cli/errors.go`; and register asserting `TestDocClaim_*` tests in `internal/cli/docs_sweep_test.go` that exercise the failure paths at the user-visible boundary.

## Open questions (Phase 2)

Plan-stage, non-gating: (1) doctor's remote-status block format (JSON shape + human rendering, informed by Phase 1 doctor output); (2) worker telemetry — whether the orchestrator should log a worker's `act` fetch timeouts to surface flaky networks early (probably yes, via a small dispatch-loop hook).
