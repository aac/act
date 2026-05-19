# Phase 2 plan v2 — architect review

**Reviewer perspective:** senior staff engineer / architect, pre-implementation review (the second of two plan-review gates). Not the cold-eye seat.
**Plan under review:** `docs/coordination-plane-phase2-plan.md` v2 (commit `ff364cc`).
**Brief:** `docs/coordination-plane-phase2-design.md` v4 (commit `a7f1bd1`).
**Phase 1 as-built:** `docs/coordination-plane-design.md` v2.1.
**Filter:** >70% confidence. Severity tagged. No taste-level nits.
**Verdict (TL;DR):** `plan-ready` with two **should-fix** items the orchestrator can fold into ticket bundles at dispatch, not gate iteration on. The v1 carryover is substantially clean.

---

## 1. Per-ticket assessment

For each of the thirteen tickets: scope soundness, AC quality, test-plan adequacy, files-touched accuracy (cross-checked against the codebase), dep-graph correctness.

**Ticket 1a (`act remote enable/disable` + config + hook skeleton).** Scope sound. ACs are eight rows of `git config` / `test -f` shell assertions — directly checkable. The `act.role=orchestrator` pin (and the unset-defaults-to-worker safety) is the right call. Files touched look right: no `cmd/act/remote.go` exists yet; new file fits. **Must-fix #1 (severity: should-fix):** the plan says "no new error codes introduced by 1a" but `act doctor` post-enable verification is in the acceptance ("returns zero findings"). If doctor is dispatched cold against a half-enabled state — e.g. 1a partway through a manual sequence — what envelope does it emit? Worth pinning a one-line behavior expectation now or explicitly deferring it to ticket 9 (which extends doctor anyway).

**Ticket 1b (add-upstream + `--force-public` + `upstream_public`).** Scope clean. AC pins the literal stderr string `refusing public upstream; pass --force-public to override` — discipline-compliant. `internal/config/upstream_patterns.go` as a new file is good; centralizes the curated list. Files touched correct. Test plan names `internal/testfixtures/remote.go` from ticket 2 — dep edge to 2 is implicit but not in the graph; **consider:** make explicit (1b → 2 for the bare-repo fixture).

**Ticket 2 (push-retry helper + fetch-rebase helper + fixture-remote owner).** This is now the convergence point. Scope is large but coherent — three coupled pieces of infrastructure. The shallow+repeated-contention exhaustion AC (synthesis S5) is genuinely the most important new test case in the plan; specifying it in the ticket where the helper lives is correct. `gitops.FetchAndRebase` pinned and named here, consumed by 3a/5/6b. Test plan adequate. **Should-fix #2:** AC says "`internal/testfixtures/remote.go` exposes a documented public API used by at least one test in this ticket and ready for import by 1b/6a/6b/7/11." "Ready for import" is unverifiable from outside the ticket. Replace with a concrete check: a smoke import-test in the fixture package itself (`internal/testfixtures/remote_test.go` includes a no-op `Test_API_Surface` that compiles against the published API), so the moment 1b/7 attempt the import they get a compile error if the surface drifts.

**Ticket 3a (push-on-write integration).** Scope is sound conceptually, but **the files-touched claim is materially overstated**, severity: **should-fix**. The plan says it edits `internal/gitops/gitops.go` plus all six write-path command files (`create.go`/`close.go`/`update.go`/`depadd.go`/`reopen.go`/`delete.go`). On inspection: `internal/cli/util.go` already centralizes commit-and-push in `WriteOpAndAutoCommit` / `WriteOpsAndAutoCommit` (lines 143, 238) with an existing `opts.Push` flag (lines 144, 211, 248, 310). Of the six command files, only `close.go` has a non-helper `gops.Commit()` call (line 438). The realistic blast radius is `util.go` + `close.go` + `gitops.go` (+ maybe the Push() helper's signature). The "do not run in parallel with any other write-path-touching ticket" sequencing rule still applies, but the conflict-surface footprint is closer to three files than seven. Acceptance row "all six write subcommands invoke `PushWithRetry` exactly once per successful commit" is correct and well-formed — it's the *files-touched manifest* that's wrong. The orchestrator's bundle won't fail because the prompt is over-broad, but the dep edge from 5 ("file overlap on `internal/gitops/gitops.go`") still holds even if the six-command-file claim shrinks. Fix: re-read the files-touched line before dispatching 3a, narrowing to the actual diff surface.

**Ticket 3b (`--offline` + slow-write logging).** Pinned schema is good. Pinned stderr literal `act: slow write detected (1247ms > 1000ms threshold); see .act/.slow-writes` is exactly the right shape: literal prefix and suffix are stable, the milliseconds are wildcarded. Fault-injection hook (`ACT_TEST_SLOW_COMMIT_MS`) named, gated to `acttest` build-tag — good hygiene. AC for the cap-at-100 pruning is asserted concretely. **Consider:** `.act/.pending-pushes` retention/format is not pinned the way `.act/.slow-writes` is. The AC says a record is added with "the local commit's SHA" but the file format itself (JSON-lines? one SHA per line? schema?) is unstated. Lower stakes than `.slow-writes` because only the next non-offline write reads it and that consumer is the same ticket, but it's still a user-visible file that doctor may eventually inspect.

**Ticket 5 (read TTL cache).** Scope clean. ACs verifiable via `FETCH_HEAD` mtime. The dep edge to 3a (same-file rebase) is correctly expressed. The "post-rebase invariant: fold-checkpoint deletion" AC is concrete. **Consider:** "`act ready --no-cache` is an alias with the same behavior" is a doc claim — it needs a `TestDocClaim_*` entry asserting both flags appear in `--help` and behave identically. Test plan mentions "Spec/test-discipline entry for the TTL behavior and the bypass flags" but plural-bypass is one entry not two; tighten.

**Ticket 6a (sync subcommand + post-receive hook content).** Scope sound. Three ACs with concrete envelopes and literal stderr (`no origin-upstream configured; run 'act remote add-upstream <url>'`). The "filesystem-watch with bounded timeout" verification (synthesis C4 W1) is the right mechanism. **Should-fix the implicit reverse dep:** AC says "1a's `act remote enable` re-reads the template — this is the file 1a touched." That's a reverse-direction edit on 1a's `internal/cli/remote.go`. Either 1a ships with a templating stub that 6a fills in, or 6a edits 1a's file post-hoc. The plan describes the latter ("hook-content template added; 1a's `act remote enable` re-reads the template"). Better to make this an explicit small re-touch ticket or co-located patch; the current shape risks 1a and 6a's commits playing tag in `internal/cli/remote.go`.

**Ticket 6b (orchestrator-write upstream-sync trigger).** Scope minimal — exactly one integration point in `internal/gitops/gitops.go`. ACs use filesystem-watch on `.act/.sync-log` mtime — concrete and verifiable. The legacy-unset-treated-as-worker AC is the right safety default. Dep edges (3a + 6a + rebases on 5) are correct.

**Ticket 7 (`bootstrap-worker --from-remote`).** Scope clean. ACs concrete: timeout envelope (`bootstrap_timeout`, exit 4), non-empty-target envelope (`target_not_empty`, exit 2), `act.role=worker` post-clone assertion. **Must-fix the implicit dep (severity: should-fix):** the plan says "blocked-by 1a." That's necessary but insufficient. Ticket 7 also depends on **ticket 2**, because the test plan invokes `BareRemote.PauseTransfer()` which ticket 2 ships in the fixture package. The dep graph in §"Dependency graph" doesn't draw the 2 → 7 edge — only "after 1a lands: 2, 1b, 7 in parallel (three workers; provably disjoint files)" in the parallelization opportunities section. But 7 cannot meaningfully test against `PauseTransfer()` until 2 ships it. The disjoint-files claim is true at the production-code level; at the test-code level, 7 has a hard dep on 2. The orchestrator should not dispatch 7 alongside 2.

**Ticket 8 (harvest narrowing + idempotency).** Scope clean. Detection rule `act.role=worker AND origin matches` is the right and robust shape — drops v1's origin-match-alone heuristic. Literal stderr `harvest skipped, worker was push-attached` is pinned. **Must-fix #3 (severity: should-fix):** the ticket says "blocked-by 7" but the *implementation* depends on Phase 1.5 harvest existing — and on inspection, no `harvest.go` exists in the main worktree. The Phase 1.5 umbrella (`act-b77a80`) is the actual dep. Through 1a's chain (1a depends on `act-b77a80`), this is transitively satisfied. But for an orchestrator picking up 8 with 7 just landed and `act-b77a80` still in-flight (possible if Phase 1.5 wasn't fully drained before plan v2 dispatches), the worker will fail trying to extend a non-existent `internal/cli/harvest.go`. Make the dep explicit: `8 blocked-by act-b77a80` in addition to `8 blocked-by 7`. Same comment as v1's S6 carryover but applied to ticket 8 specifically — the v2 fix only added the hard edge at 1a, not at 8.

**Ticket 9 (doctor extensions).** Scope clean. Three literal stderr strings pinned (`local: 2 unpushed commits ahead of origin`, `remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity`, `upstream: origin-upstream is 60 commits behind origin; run 'act remote sync'`). Case (g) slow-write summary asserts against the schema 3b pins — clean cross-ticket discipline. Five new registry entries called out. **Consider:** AC for case (h) hardcodes "60 commits" but `act.upstreamDriftThresholdCommits` is configurable (default 50). The test should drive past the threshold (51+); the AC fixture using 60 is fine but the stderr literal should match what the threshold *is*, not the count itself. Tighten: literal pattern is `upstream: origin-upstream is <N> commits behind origin; run 'act remote sync'`, and the test fixture passes a known N.

**Ticket 10 (orchestrate doc).** Scope clean. The symlink-trap mitigation is well-handled — the cross-repo commit-and-push step is in the worker-prompt template, the AC asserts the claude-config repo's `git status` clean and `@{u}` matches HEAD, and the act-side commit references the claude-config SHA. **Consider:** the AC "the act repo's commit for ticket 10 references the claude-config commit SHA in its body so the cross-repo link is traceable" is novel and good — there's no `TestDocClaim_*` for it because there can't be (it's a commit-message assertion checked at close-time, not a sweep claim). Worth flagging this to the implementer as a manual close-time verification.

**Ticket 11 (E2E integration).** Scope sound. Five named E2E behaviors. CI runtime budget (2 minutes) is realistic. Test plan adequate. No new findings.

---

## 2. Critical-path analysis

The plan claims worst-case 13 cycles, aggressive 8 cycles. The aggressive case rests on `act-b77a80 → 1a → 2 → 3a → {5 rebase, 3b rebase, 6a parallel, 8 after 7, 6b after 6a}`. Two observations:

**Hidden serial constraint: `internal/gitops/gitops.go` carries 3a → 5 → 3b → 6b in sequence**, four sequential tickets in one file. The plan acknowledges this in §"Dependency graph" ("3a → 5 → 3b → 6b in that file"). The plan's "after 3a lands: 5, 3b, 6a in parallel" comment in §"Parallelization opportunities" contradicts the same-file rebase chain stated two paragraphs earlier. They cannot run literally in parallel — the wording should be "after 3a: 6a runs in parallel; 5, 3b, 6b sequence on `internal/gitops/gitops.go`." Severity: **consider** (it's a wording fix, not a sequencing error).

**Genuine parallelization opportunity the plan misses:** ticket 1b and ticket 2 can dispatch *together* after 1a — both edit `internal/cli/remote.go` only at the new-file level (1b adds `remote_upstream.go`, 2 adds nothing in `internal/cli/remote.go`). The plan lists "after 1a lands: 2, 1b, 7 in parallel (three workers; provably disjoint files)" — that's correct. Combined with ticket 7 (also after 1a, file-disjoint), this is three workers. Good.

**Critical path is honest.** `act-b77a80 → 1a → 2 → 3a → 6b` is six serial steps; nothing in v2 contrives parallelism that isn't there.

---

## 3. Risks not flagged

Stress-testing the v2 plan against the prompts:

**Residual orchestrator-detection mechanism (`act.role`).** Closed cleanly. `act remote enable` writes `orchestrator`, `bootstrap-worker --from-remote` writes `worker`, unset defaults to `worker`, no path heuristic remains. The v1 architect concern about symlink/NFS/extracted-state failure modes is fully addressed by reading config instead of inspecting paths. The one remaining edge case: a user (Andrew) running `git config` by hand to set `act.role=orchestrator` on a worker clone. The plan doesn't say `act doctor` validates role-vs-topology consistency. **Severity: consider** — file as a follow-up; not a Phase 2 must-fix.

**Slow-write retention under concurrent writes.** The `.act/.slow-writes` cap-at-100-pruning is in-place edit. Two slow writes finishing concurrently both want to rewrite the file. The plan doesn't specify locking. Under the bare-repo orchestrator topology, only the orchestrator writes from its main checkout *and* serially processes pushed ops — so cross-process write contention on `.act/.slow-writes` should be rare. But it's not impossible: a slow write on the orchestrator's own checkout overlapping with a slow worker push that also triggers a slow-write log on its clone (the file is per-clone, so the worker has its own — actually fine on second thought). **Severity: consider** the per-clone semantics: each clone has its own `.act/.slow-writes`, so doctor's case (g) summary on the orchestrator sees only the orchestrator's slow writes, not the workers'. The plan doesn't say this; the implementer should know.

**Fail-soft sync × doctor case (h).** Case (h) fires when upstream is N commits behind orchestrator. If sync is failing silently (fail-soft), case (h) eventually warns. Good. But: under `--no-fetch`, case (g) downgrades to warn. Case (h) doesn't have a `--no-fetch` downgrade path stated. If a user runs `act doctor --no-fetch` and the cached state says upstream is fresh, case (h) shouldn't false-positive. Ticket 9 doesn't address this — case (h) detection ("after a successful upstream fetch") requires the fetch, so `--no-fetch` should suppress case (h) entirely. **Severity: should-fix.** Add to ticket 9's AC: "`--no-fetch` suppresses case (h) emission."

**Shallow × push-contention.** The plan ships the test (synthesis S5) — ticket 2's "shallow + repeated-contention + exhaustion" case is exactly what the v1 architect asked for. The AC pins `details.shallow_unshallow_attempted=true` and `details.retry_count=5`. Genuinely closed.

---

## 4. Spec-discipline compliance

Tickets with user-visible behavior and their required `TestDocClaim_*` plumbing, audited against the plan:

| Ticket | New user-visible surfaces | `TestDocClaim_*` named in plan? |
|---|---|---|
| 1a | `act remote enable` / `disable` flag behavior, `act.role` config schema | implicit ("each acceptance row has a corresponding TestDocClaim_*"); fine |
| 1b | literal stderr `refusing public upstream...`, `upstream_public` envelope | yes, explicit |
| 2 | `push_exhausted` envelope, `remote_unreachable` envelope | yes, two registry entries called out |
| 3a | (none net-new; uses 2's envelopes) | not required |
| 3b | slow-write stderr literal, `.slow-writes` schema, cap-at-100, `--offline` flag | three entries called out |
| 5 | TTL behavior, `--fresh`/`--no-cache`, `ACT_DISPATCH_MODE` bypass | partial — needs separate entry for `--no-cache` alias (§1 above) |
| 6a | `upstream_not_configured` envelope, sync-log schema | two entries called out |
| 6b | (no new error codes) | not required |
| 7 | `bootstrap_timeout`, `target_not_empty`, `--from-remote` flag | yes |
| 8 | `harvest skipped...` stderr literal | yes |
| 9 | five literal stderr lines (cases f, g×2, h, slow-write summary) | five registry entries called out |
| 10 | orchestrate.md dispatch step, cutover note, failure-mode doc | three entries called out |

Net: **one gap** — ticket 5's `--no-cache` alias claim is implied but not pinned to a registry entry. Easy fix in the ticket bundle.

The plan's cross-cutting constraint replacing v1's ticket 4 is well-stated and discipline-compliant. The `CLAUDE.md` rule ("the claim and the test ship in the same commit") is respected throughout.

---

## 5. Verdict + reasoning

**`plan-ready`** with the should-fix items folded into the ticket bundles at dispatch. None require another plan-iteration cycle.

The should-fix list (smallest set of changes for clean dispatch):

1. **Ticket 3a's files-touched manifest is overstated.** The realistic surface is `util.go` + `close.go` + `gitops.go`, not all six write-path command files. Tighten before dispatch; the AC stays.
2. **Ticket 7 needs an explicit `blocked-by 2` edge** (it consumes `BareRemote.PauseTransfer()`); the current "parallel after 1a" claim is true production-code-wise, false test-code-wise.
3. **Ticket 8 needs an explicit `blocked-by act-b77a80`** alongside 7 (harvest doesn't exist yet in the main worktree).
4. **Ticket 9 case (h) should suppress under `--no-fetch`** — add to AC.
5. **Ticket 2's fixture-package "ready for import" AC needs a concrete check** — add a smoke import-test in the fixture package itself.

Items 1–5 are bundle-level refinements, not plan-rewrite-level. The orchestrator can apply them in the ticket prompts when dispatching, or the plan author can patch them in place. Either path is fine; both stay inside v2.

---

## 6. What's working well (do not break)

- **The `act.role` config-key decision is the cleanest fix in v2.** Pins orchestrator detection to a single mechanism (config), eliminates path heuristics, has safe-by-default unset semantics. Don't second-guess this on a future ticket.
- **The pinned literal stderr strings throughout tickets 1b/3b/6a/8/9 are discipline-compliant and grep-friendly.** Future cleanup that "normalizes" or "improves" these messages must update the corresponding `TestDocClaim_*` registry in the same commit. The sweep test enforces this.
- **`internal/testfixtures/remote.go` as a ticket-2-owned shared package** is the right hygiene. The alternative (each consumer ticket reinventing) is what created the v1 plan's drift risk. Don't fragment this package per-ticket on a future iteration.

---

## 7. Carryover check (v1 findings)

Audited against the v1 synthesis remediation list (13 items):

- **Item 1 (orchestrator detection — `act.role`):** `was-addressed-and-fine`. Pinned in ticket 1a's schema; 6b reads it; default-to-worker safety in place.
- **Item 2 (slow-write schema):** `was-addressed-and-fine`. Schema pinned in 3b; 9 case (g) asserts against it.
- **Item 3 (split ticket 3 + add 3a→5 dep):** `was-addressed-and-fine`. Both splits and edge in graph.
- **Item 4 (AC audit for user-visible surfaces, W1/W2/W3):** `was-addressed-and-fine`. All three weakest acceptances tightened with literal strings and concrete mechanisms (filesystem-watch on `.sync-log`, exact stderr text for case (h), pinned slow-write text + schema + fault-injection hook).
- **Item 5 (drop ticket 4, renumber):** `was-addressed-and-fine`. Cross-cutting constraint paragraph replaces the slot.
- **Item 6 (ticket 2 owns fixture package):** `was-addressed-and-fine`. Bare-repo-on-filesystem shape pinned, public API outlined.
- **Item 7 (ticket 10 symlink trap):** `was-addressed-and-fine`. Cross-repo commit-and-push in worker prompt template; AC asserts both repos' state.
- **Item 8 (split ticket 1 → 1a/1b):** `was-addressed-and-fine`.
- **Item 9 (split ticket 6 → 6a/6b):** `was-addressed-and-fine`.
- **Item 10 (disable removes hook file):** `was-addressed-and-fine`. AC asserts `os.Stat` returning ENOENT.
- **Item 11 (shallow + contention + exhaustion test):** `was-addressed-and-fine`. Ticket 2 AC pins the case with `details.shallow_unshallow_attempted=true`, `details.retry_count=5`.
- **Item 12 (Phase 1.5 hard dep at 1a):** `was-fixed-but-incompletely`. The edge is present at 1a → `act-b77a80`. But ticket 8 (which extends the Phase 1.5 `harvest.go` that doesn't exist in main yet) lacks the same explicit edge; it relies on transitivity through 1a, which the orchestrator's dep walker may or may not respect at dispatch time. Make it explicit on 8.
- **Item 13 (`gitops.FetchAndRebase` factoring):** `was-addressed-and-fine`. Pinned in ticket 2; consumed by 3a and 5.

**Net carryover assessment:** 12 of 13 items closed cleanly; item 12 fixed-but-incompletely (the explicit edge addition I flag above in §1/§5).

**Residual v1 issues to call out before merge:** just the one — ticket 8's missing `blocked-by act-b77a80` edge. Either patch in v2 or instruct the orchestrator to refuse dispatch of 8 until `act-b77a80` is closed.

---

**Word count:** ~2280.
