# Coordination Plane Design — act-f048

_2026-05-17. Design note for the coordination-plane reframe. Supersedes the multi-env research issue (act-8d67). Subsumes the deferred quiet-the-op-log brainstorm; obviates most of `docs/commit-noise-design.md`'s residual Phase-2 territory._

## Reframe

**Act is contributor infrastructure, not project history.**

Today, `.act/` lives inside the code repo and its op-log is committed to the same branch as code. Every claim/close/create produces an `act-op:` commit on `main`. In active repos this means ~70% of `git log` is act bookkeeping (see `docs/commit-noise-design.md` for the measured baseline; the shipped `per_session` bundling work (act-728d / act-a659) reduced lifecycle commits modestly but did not change the order-of-magnitude shape). The problem is architectural, not tactical: tactical fixes (bundling, side-refs, log filters) all leave act state inside the public history.

The right move is structural: act state lives outside the host project's shared git history. Specifically, `.act/` becomes a nested git repository, gitignored from the surrounding code repo. Work commits on the code repo's main branch continue to carry `(act-XXXX)` markers — those markers are the only act-shaped artifact that survives in the public history, structurally analogous to a `(JIRA-1234)` reference. Everything else (ops, claims, dependency edges, op-log history) lives in the nested act repo.

Two corollaries fall out:

1. **Code is canonical; act is overlay.** Work commits with markers are the durable record of what work was actually done. The act op-log is a coordination layer that can desync, be wiped, or be unavailable on a given machine — and the code history survives intact. `act doctor` becomes a reconciler that cross-references the two.
2. **Scope is operator-decided, not project-bound.** The boundary "agents that should see each other's claims" sometimes maps to one code repo, sometimes to several (multi-repo project), sometimes to a subset of one (different teams on different parts of a monorepo). The operator picks the boundary when they `act init` or when they hand state to an agent in its prompt. Pairing with a code repo is convention, not architecture.

The reframe also achieves the OSS-adoption property that's been blocking public release of ask/act/poke: a contributor cloning the public repo gets exactly the code, with no act state visible.

## Two-phase shippability

The full reframe naturally splits into two independently-shippable phases. Phase 1 is on the critical path for public release of ask/act/poke. Phase 2 unblocks the distributed-agent case but is not required for public release.

### Phase 1 — local-only

`.act/` is a nested git repo, gitignored from the surrounding code repo. State lives entirely on one machine. Concurrent agents in the same working tree continue to coordinate via act's existing atomic-op semantics. The host project's `git log` becomes pristine of act-ops.

Phase 1 is sufficient for any project that runs all its agents in one place. That includes every project on the current public-release path. It does not address the case where one contributor's agents run on multiple machines (laptop + Claude Code on web + dev sandbox + CI) and need to see each other's claims — that's Phase 2.

**Why Phase 1 is dramatically simpler than the full design:** with no cross-machine sync, most of the reconciliation territory dissolves. There's no sync-failure semantics (nothing syncs). There's no multi-environment doctor (one environment). Doctor's only job is the local cross-check: walk the code repo's `git log` for markers, walk the local act state, report anomalies. No reconciliation contract is required between two divergent act states because there's only one.

### Phase 2 — contributor-replicated

A contributor can configure an act remote (default off). When configured, their agents on any machine sync state against that remote via `act sync` (and implicitly on `act close --push`). Cross-machine coordination uses `git push/pull/merge` on the nested act repo's history. The remote is private by default and owned by the contributor; the code repo's remote knows nothing about it.

Phase 2 is where the harder design questions live (sync failure semantics, two-remote push fanout, marker collision across multiple act states). Phase 1 doesn't depend on any of them being resolved.

## Invariants

These hold under both phases:

- **No act state in shared project history.** No `.act/ops/*` files committed to the host repo, no `act-op:` commits in the host repo's history. The host project's `.gitignore` includes `.act/`. This is the load-bearing property; the design exists to enforce it structurally.
- **`(act-XXXX)` work-commit marker convention survives.** Work commits authored by agents continue to include the marker. Markers are useful provenance and load-bearing for doctor reconciliation.
- **Atomic op semantics preserved.** Claim, close, create, dep-edge writes remain atomic and durable. Today's "git as the database" insight is fine to keep; the change is which git repo the writes go to.
- **Multi-agent concurrent claim merging continues to work.** Two agents claiming simultaneously in the same act state resolve via the same conflict-free mechanism act uses today. (In Phase 1: same working tree. In Phase 2: same act remote.)
- **CLI surface mostly preserved.** `act ready`, `act update --claim`, `act close --reason`, etc. work unchanged. Phase 2 adds `act sync` and remote configuration commands. Agents don't relearn the loop.
- **Audit trail per-act-state.** The act state's own history (claim sequences, who-closed-what, HLC) is fully queryable by `act log`, `act show`, `act doctor`, etc. — it just lives in the nested act repo's history, not the code repo's.
- **No mandated cross-contributor visibility.** The default scope is "the agents of one operator." Multi-contributor shared coordination is possible via a shared act remote (Phase 2) but is not the default. The architecture doesn't impose visibility either way; the operator chooses.

## Doctor reconciliation

The shape of the reconciliation contract is the central design point that's *not* what the previous draft of act-f048 implied. The previous draft enumerated five edge cases as load-bearing and required deterministic rules for each. On reflection, only one is an actual anomaly; the others are normal states that doctor needs to recognize as legitimate. The full enumeration, with honest severity:

| Case | Severity | Behavior |
|---|---|---|
| (a) marker in code, no matching issue in act state | Real but rare; primarily Phase-2 | Report as "unresolved marker — consider `act sync`" if remote configured, else "consider investigating" |
| (b) issue in act state, no closing marker in code | **Normal state** (tracking-only, wrong-claim close, no-code close) | Not an anomaly. Doctor ignores. |
| (c) multiple markers on different commits for same id | **Normal state** (multi-commit work, fix-up commits) | Not an anomaly. Doctor ignores. |
| (d) marker referencing unknown id | Rare (typo, deleted issue, cross-state reference) | Report as warning; agent investigates |
| (e) claims newer than most recent code commit | **Normal state** (work in progress) | Not an anomaly. Doctor ignores. |

The actual contract is much smaller than the previous draft suggested: **doctor lists markers in the code log, cross-references against act state, reports (a) and (d) as warnings, ignores (b)/(c)/(e) as legitimate.** Doctor is liberal in what it accepts and clear about what's actually unresolved. We don't need a worked spec of all five before implementing.

This matters because the previous draft made reconciliation sound like a multi-week design problem gating implementation. It isn't.

## Implementation delta

The delta from today's act, by phase:

### Phase 1
1. **`act init`** does a `git init` inside `.act/`; if it detects a surrounding git repo, appends `.act/` to that repo's `.gitignore`.
2. **Write paths** (claim, close, create, dep, etc.) target the nested `.act/` git repo, not the surrounding code repo. The `git add` / `git commit` logic in `internal/store/` already isolates the working tree; it just needs to operate on `.act/`'s tree.
3. **`act doctor`** learns to scan the surrounding code repo's `git log` for `(act-XXXX)` markers and report the (a)/(d) anomalies per the table above. No cross-machine concerns yet.
4. **Migration of existing repos** (this repo, plus downstream like inbox-triage, aac-website, sift, poke): one-time conversion. Existing op files in code-repo history stay there as the "before" record (no rewrite — audit trail is valuable). The nested act repo starts fresh, or imports the existing op files as an initial bulk commit. Decision per-repo.

### Phase 2 (later)
5. **`act sync`** — push/pull the nested act repo against its configured remote. Bundle into `act close --push` and similar verbs when remote is configured.
6. **Remote configuration commands** — `act remote add/remove`, persisted in the nested act repo's `.git/config`.
7. **Doctor cross-repo** — take one or more code-repo paths as input; useful when an act state coordinates work across multiple code repos.
8. **Sync-failure semantics spec** — agent contract for "code pushed but act sync failed" and vice versa. Probably: doctor surfaces it on next reconcile, agent re-runs sync.

Phase 1 alone is probably 3-5 implementation issues. Phase 2 is a separate batch, sized when we get there.

Everything else — op file format, HLC, claim protocol, idempotency, the canonical loop's shape — stays as it is. Agents experience Phase 1 as "act state lives in a different git repo now, gitignored from your project repo"; the loop steps don't shift.

## Migration story

- **This repo (act).** Highest-stakes migration because we dogfood here. Plan: ship Phase 1 behind a flag (`act init --nested` or similar) so the migration is opt-in initially. Switch the dogfood loop over in one transaction (one-time `git init .act/`, append `.act/` to `.gitignore`, leave historical op commits in the code repo as the "before"). Once exercised for a few days, flip nested to the default for new `act init`.
- **Downstream repos** (inbox-triage, aac-website, sift, poke, ask). Each gets the same one-time conversion. Phase 1 only. No remote, single-machine.
- **Already-public-history act-op commits in any repo.** Leave them. The point of the reframe is forward-going pristine history; historical noise is not a problem worth rewriting history to fix.
- **The deferred quiet-the-op-log brainstorm** is closed by this design — the directions it flagged (git notes, sidecar ref, more aggressive bundling) are all subsumed by "ops aren't in the code repo at all."

## What is NOT changing

- The canonical work loop's shape (`ready → claim → work → close → push`). The push at step 7 still pushes the code repo's branch to its remote. Phase 2 adds an implicit `act sync` alongside, but it's invisible at the loop-step level.
- Op file format, HLC, claim protocol, idempotency guarantees.
- The `(act-XXXX)` marker convention. Load-bearing under the new design.
- `bundle_strategy=per_session` (act-728d / act-a659). Still relevant for whatever lifecycle commits do happen in the nested act repo, even though they no longer pollute the code repo.
- Phase 1 of the four-phase orchestration plan (agent-push-to-main). Orthogonal — the classifier blocking `git push` is a harness/permission problem unrelated to where `.act/` lives.

## Open questions (genuinely open)

The questions left for implementation discussion, after the trimming above:

1. **Migration default — nested-from-day-one or opt-in flag first?** Argues for opt-in flag: lower blast radius if the nested-repo behavior surprises us, especially around the existing close-stages-into-work-commit semantics (act-a659) where the staging targets the code repo today. Argues for nested-as-default: it's the whole point of the reframe, and a flag we'll flip in two weeks anyway is just noise. Default position: ship as the new default for `act init`; provide `act init --inline` as the escape hatch for the one weird case (probably none) where someone wants the old shape.
2. **Phase 1 doctor's anomaly threshold.** When doctor encounters an (a) anomaly (marker without matching act state), does it exit non-zero or just warn? Probably warn-only by default with `--strict` for the CI-gate case, but worth confirming against actual repos before locking it in.
3. **Phase 1 migration script's existing-op import.** Two paths: (a) leave existing `.act/` op files in place, `git init` over them, commit as the nested repo's initial state; (b) treat the nested repo as fresh and let the on-disk op files be the source of truth without an initial commit. (a) gives a clean import-commit per existing issue, (b) is simpler but loses the boundary marker. Probably (a), but worth confirming.
4. **Phase 2 sync-failure semantics.** Deferred to Phase 2 design pass. Not blocking Phase 1.
5. **Phase 2 marker uniqueness across multiple act states.** Deferred to Phase 2 design pass. Random-hex width is probably sufficient; worth confirming when we get there.

Questions explicitly *not* on this list any more: the five-edge-case reconciliation contract (resolved in the doctor section above), the cross-repo doctor (deferred to Phase 2), the per-contributor-only invariant (broadened to operator-decided scope above).

## Relationship to other issues

- **act-8d67** (research: separate-repo or separate-remote model): supersedes. Close as superseded when this design lands.
- **act-6c73** (pure-metadata bundling for inbox-triage): largely dissolves under Phase 1 — if op commits aren't in the code repo, the bundling concern evaporates. Re-evaluate scope when Phase 1 lands; probably close as obsoleted.
- **act-6018** (commit noise design note): the residual Phase-2 work in that doc (further bundling, side refs) becomes obviated. Add a pointer note at the top of `docs/commit-noise-design.md` linking here.
- **act-208e** (orchestrator-scoped bundle_strategy): re-evaluate. The bundling pressure that motivated it largely evaporates once ops aren't in the code repo. May still be relevant for the nested act repo's own history, but the urgency drops.
- **act-dfa5** (noise-related): re-evaluate as above.
- **The deferred quiet-the-op-log brainstorm memory**: closed by this design. Update the memory entry to point at this doc.

## Acceptance for act-f048

This doc constitutes the design note. Closure requires:

1. ✓ Design note in repo (this file).
2. ✓ Reframe + reconciliation behavior documented per the trimmed shape above.
3. External review on this design (in flight; this design must come back review-clean or be revised).
4. Phase 1 implementation issues filed (per the four-item delta in the implementation section).
5. act-8d67 closed as superseded.
6. act-6c73, act-208e, act-dfa5 re-evaluated (close-as-obsoleted, scope-revised, or kept-as-is — explicit call per issue).
7. Migration story exercised on this repo (the dogfood gate) before Phase 2 begins.
8. Phase 2 deferred to a separate design pass after Phase 1 is exercised.

Items 1-2 are done by writing this file. Item 3 is the active gate. Items 4-8 follow review.
