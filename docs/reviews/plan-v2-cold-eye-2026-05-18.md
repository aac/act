# Phase 2 plan v2 — cold-eye review

**Reviewer:** cold agent, no prior session context. Plan read first; v1 review/synthesis read only for the carryover check.
**Plan:** `docs/coordination-plane-phase2-plan.md` v2 (commit `ff364cc`). **Brief:** `docs/coordination-plane-phase2-design.md` v4 (`a7f1bd1`).
**Confidence filter:** >70%. Taste nits omitted.

## 1. Per-ticket pickup-ability

For each ticket: could an agent claim cold and execute without phoning home? Where would they get stuck?

### 1a (`act remote enable / disable` + config + hook skeleton)
**Pickup-able.** All keys, decisions, and `git config` / `test -f`-grade acceptance pinned. Stuck-point: 1a installs an *empty* `post-receive` hook; 6a fills the body in later via a re-touch (6a scope, line 237). A cold pickup of 1a won't know the empty file is intentional. Fix: one sentence in 1a saying "hook body is intentionally empty until 6a lands."

### 1b (`add-upstream` + `--force-public` + `upstream_public`)
**Pickup-able.** Public-pattern list deferred to `internal/config/upstream_patterns.go` (reasonable). Stderr literal pinned. Depends on ticket 2's `BareRemote` fixture (named).

### 2 (push-retry helper + fixture + `FetchAndRebase`)
**Pickup-able.** Densest AC in the plan; shallow+contention+exhaustion test well-specified; backoff base appropriately delegated. Stuck-point: ticket 7's acceptance (line 295) names `BareRemote.PauseTransfer()` as a "synthesized hang" helper, but the public-API enumeration in ticket 2 (line 105) lists only `NewBareRemote`, `AdvanceCommits`, `ConcurrentPush`, `InitShallow`. **Add `PauseTransfer` to ticket 2's API list.** Otherwise 2 closes "done" and 7 blocks on a missing helper.

### 3a (push-on-write integration)
**Pickup-able with a hole.** `ACT_TEST_FAIL_PUSH_AFTER=N` is named (line 141) and said to be "documented in this ticket" but never says *where* (code comment? spec? README?). Compare to 3b's parallel hook, which pins location ("`internal/gitops/gitops.go` next to the hook", line 160). **Pin 3a's hook doc-location.**

Separately, 3a ships a user-visible behavior ("writes auto-push on remote-configured projects") and its Files-touched list omits `internal/cli/docs_sweep_test.go`. The AC "all six write subcommands invoke `PushWithRetry` exactly once" is an internal-state assertion, not a user-visible-boundary one. CLAUDE.md's doc-discipline rule names README + spec entries as user-visible. **Add a `TestDocClaim_PushOnWriteRemoteConfigured` slot, or justify the omission.**

### 3b (`--offline` + slow-write logging)
**Pickup-able.** Slow-write schema and stderr literal both pinned (lines 165, 179); cap-at-100 specified with a concrete test. Gap: `.act/.pending-pushes` is named but its schema isn't pinned the way `.slow-writes` is. AC row 1 implies one-SHA-per-line, but it's not asserted, and downstream doctor cases will read this file. **Pin the schema in ticket 3b.**

### 5 (read-path TTL cache)
**Pickup-able.** TTL, bypass triggers, fold-checkpoint invalidation all named. Implicit dep missing from graph: plan prose at line 447 sequences `3a → 5 → 3b → 6b` in `internal/gitops/gitops.go`, but the graph shows 5 and 3b as parallel children of 3a. See §3.

### 6a (`act remote sync` + hook content)
**Pickup-able.** Bounded fs-watch verification mechanism named for the async test (line 244). The implied `fsnotify`-shape dependency hasn't been pre-cleared as a project dep — worth a one-liner authorizing "fsnotify or equivalent". The "1a is re-touched to read the template" thread means 6a edits behavior 1a shipped; file overlap is acknowledged in Files-touched.

### 6b (orchestrator-write upstream-sync trigger)
**Pickup-able.** `act.role` read on commit; orchestrator fires sync, worker doesn't; unset defaults to worker. AC asserts via sync-log mtime advances. Implicit dep missing from graph: line 271 says 6b "rebases on 5 since 5 has already rebased on 3a." Graph shows 6b blocked only by 3a + 6a. See §3.

### 7 (`bootstrap-worker --from-remote`)
**Pickup-able, contingent on the `PauseTransfer` fix in ticket 2.** AC for "concurrent bootstraps to different target paths succeed without interfering" (line 296) doesn't define *what* interference is being checked. Suggest: "asserted by N parallel bootstraps, each followed by `act ready`, all returning the same state." The `act-b77a80 → 1a` rationale on plan line 30 actually justifies an edge at 7 (which extends Phase 1.5's `--import-from`), not at 1a; see §3.

### 8 (harvest narrowing)
**Pickup-able.** Four named cases, "harvest skipped" literal pinned, detection mechanism (`act.role=worker` AND `origin` matches) concrete. Mini under-spec: how does the orchestrator resolve "its own canonical `.act/.git` path" for the origin-match? Presumably from the CWD of the harvest invocation. A one-liner would prevent over-engineering.

### 9 (doctor extensions)
**Pickup-able.** Five new cases with literal stderr strings and JSON-block fields. Implicit dep missing: case (g) emits the `remote_unreachable` envelope code, which is introduced in ticket 2 (not 3b or 6a). Transitive ordering works; the missing edge is documentation-completeness only.

### 10 (`/orchestrate` doc + symlink trap)
**Pickup-able.** Cross-repo commit-and-push step is in the worker prompt template; three-way AC (orchestrate.md content + claude-config pushed + act-side commit references claude-config SHA) is enforceable. Gap: the act-side test reads the symlink target via `os.Readlink` (line 377), but on a CI runner the target won't exist (no Andrew's claude-config). **The plan should pin behavior: skip on `fs.ErrNotExist`, or gate to Andrew-only runners.** Otherwise first non-Andrew CI run fails.

### 11 (E2E)
**Pickup-able.** Five named scenarios, 2-minute CI budget, out-of-scope list explicit. Worth stating "SSH transport not exercised here; covered separately" — otherwise an agent might add SSH cases and bust the budget.

## 2. Over- / under-specified

**Under-specified** (plan defers real decisions):
- 3a's `ACT_TEST_FAIL_PUSH_AFTER` documentation location.
- 3b's `.act/.pending-pushes` schema.
- 7's interference-test definition.
- 10's CI-runner symlink-missing behavior.
- 8's orchestrator-path resolution mechanism.

**Over-specified** (plan doing the implementation's job):
- Ticket 2's `gitops.FetchAndRebase` typed-error enumeration (`ErrRebaseConflict`, `ErrShallowExhausted`, `ErrFetchFailed`, line 104). The plan should commit to "named typed errors for retry-or-bail" and let implementation name them.
- Ticket 1a's default values for seven config keys are now plan-frozen. A small tweak (`bootstrapTimeoutSeconds=60`?) becomes a plan-amendment ticket.

Neither over-spec is harmful; both could be loosened without cost.

## 3. Implicit deps the graph doesn't capture

1. **5 → 3b** (`internal/gitops/gitops.go` file overlap). Plan prose (line 447) sequences `3a → 5 → 3b → 6b` in that file; the graph (line 410ff) shows 5 and 3b as parallel children of 3a.
2. **5 → 6b** (same file). Line 271 says "6b rebases on 5"; graph doesn't.
3. **2 → 9** (envelope code `remote_unreachable`). Transitive ordering works via 3b; the explicit edge would close the loop for a cold pickup.
4. The `act-b77a80 → 1a` hard-edge rationale on line 30 ("ticket 7 extends `--import-from`") justifies an edge at 7, not 1a. Either move the edge to 7 (lets 1a/1b/2 start in parallel with Phase 1.5's tail) or rewrite the rationale.

For a plan whose value proposition is "executing agent picks up any ticket cold," graph/prose mismatch is the worst-case defect class.

## 4. Open implementation questions the plan should settle

Only worker telemetry is listed open — correctly deferred. But the following are plan-stage, not implementation-stage:
- 10's CI-machine symlink behavior (§1 ticket 10).
- 6a's `fsnotify`-or-equivalent dependency (§1 ticket 6a).
- 3b's `.pending-pushes` schema (§1 ticket 3b).

None blocks the plan; all will surface in cycle one if not pinned.

## 5. Surprises

**Positive.** The 1/3/6 splits land on real file-overlap boundaries, not ticket aesthetics. The `act.role` config-key decision (replacing v1's path heuristic) is a clean fix with safe-by-default unset semantics. Literal stderr strings throughout (1b, 3b, 6a, 8, 9) make `TestDocClaim_*` slots mechanical. The shallow+repeated-contention+exhaustion test in ticket 2 is genuinely hard to think of cold. The symlink-trap acceptance asserting both repos' state is materially better than v1.

**Negative.** The 1a/6a hook handoff leaves a non-functional intermediate state on `main` between commits — `act remote enable` installs an empty hook, workers push, orchestrator doesn't sync. The plan doesn't acknowledge this interval. Consider gating 1a's land-on-main on 6a being ready, or having `act remote enable` warn "hook body not yet shipped" until 6a lands. Separately, the graph/prose mismatch in §3 is exactly the failure mode a cold-pickup plan can't afford.

## 6. Two summaries

**What this plan delivers.** Thirteen tickets across four phases (foundation, write path, read path/sync, bootstrap/harvest/doctor/integration) turning brief v4 into executable work. Config keys, envelope codes, literal stderr strings, file schemas (`.slow-writes`), and `TestDocClaim_*` slots are pinned at user-visible boundaries. The plan owns the cross-cutting doc-discipline rule, the shared `internal/testfixtures/remote.go` fixture package, and the `gitops.FetchAndRebase` extraction. Most ordering is captured in the dep graph; parallelism opportunities named per phase. Wall-clock estimate (6–12 days) grounded in the repo's existing dogfood rate. Two new error codes (`push_exhausted`, `remote_unreachable`) plus four more from new tickets; all routed through the envelope + sweep registry.

**What I'd warn the implementer about.** The 1a/6a hook handoff produces a non-functional partial state between commits — land them in the same release window. The dep graph misses three concrete edges (5→3b, 5→6b, 2→9) and one rationale mismatch (Phase 1.5 → 1a vs. → 7); read the prose at line 447 alongside the graph or the orchestrator will mis-order. Five tickets have one-or-two-line under-specs that will surface in cycle one: `.pending-pushes` schema, `ACT_TEST_FAIL_PUSH_AFTER` doc location, ticket 7's interference test, ticket 10's CI behavior, ticket 8's orchestrator-path resolution. None block claiming; all save 10 minutes of guesswork.

## 7. Carryover check (v1 findings)

Read after writing §§1–6. Synthesis listed thirteen items (C1–C6 convergent + S1–S7 single-reviewer).

| # | Item | Status |
|---|---|---|
| C1 | OQ #4 (orchestrator detection) → `act.role` | **addressed-and-fine.** Pinned in 1a; read by 6b; written by 7; defaults to worker on unset. |
| C2 | OQ #1 (slow-write log schema) | **addressed-and-fine.** Pinned field-by-field in 3b; referenced by 9 case (g). |
| C3 | Split ticket 3 + add 3a→5 dep | **fixed-but-incomplete.** Split is done; 3a→5 in the graph. The plan-prose sequencing `5 → 3b → 6b` (line 447) is NOT in the graph. See my §3. |
| C4 | AC user-visible-surface audit (W1/W2/W3) | **addressed-and-fine.** Bounded fs-watch (6a), case-(h) stderr literal (9), slow-write stderr literal + schema + fault-injection (3b). |
| C5 | Drop ticket 4 | **addressed-and-fine.** Cross-cutting constraint moved to plan prose (line 20). |
| C6 | Ticket 2 owns `internal/testfixtures/remote.go` | **fixed-but-incomplete.** Ownership pinned; API enumerated. `PauseTransfer` used by ticket 7 is missing from the enumeration. See §1 ticket 2. |
| S1 | Ticket 10 symlink trap | **addressed-and-fine.** Own subsection; AC asserts both repos. My CI-machine residual is a new finding, not v1 carryover. |
| S2 | Split ticket 1 into 1a/1b | **addressed-and-fine.** |
| S3 | Split ticket 6 into 6a/6b | **addressed-and-fine.** |
| S4 | `act remote disable` removes hook file | **addressed-and-fine.** Filesystem `test -f` check is AC row 6. |
| S5 | Shallow+contention+exhaustion test | **addressed-and-fine.** Named AC row in ticket 2. |
| S6 | Phase 1.5 dep as hard edge | **fixed-but-incomplete.** Edge in the graph; rationale text (line 30) justifies the edge at 7, not 1a. |
| S7 | `gitops.FetchAndRebase` extraction in ticket 2 | **addressed-and-fine.** Pinned in 2; consumed by 3a and 5. |

**Carryover summary.** 10/13 addressed-and-fine, 3/13 fixed-but-incomplete (C3 dep edges, C6 missing API entry, S6 rationale placement), 0/13 not-addressed.

---

## Verdict

**`needs-iteration`** — barely. All remaining issues are tightening, not structural.

**Smallest set of changes to reach `plan-ready`:**

1. Add three implicit dep edges (or rewrite the dep-graph caption to acknowledge file-overlap sequencing): `5 → 3b`, `5 → 6b`, `2 → 9`. (§3, fixes C3 carryover.)
2. Add `PauseTransfer(...)` to ticket 2's public-API enumeration. (§1 ticket 2, fixes C6 carryover.)
3. Either move the `act-b77a80` edge to ticket 7, or rewrite line 30's rationale to justify the 1a placement. (§3, fixes S6 carryover.)
4. Pin `.act/.pending-pushes` schema in ticket 3b. (§1 ticket 3b.)
5. Pin `ACT_TEST_FAIL_PUSH_AFTER` documentation location in ticket 3a. (§1 ticket 3a.)
6. Add a one-liner to ticket 10 covering CI-machine symlink-missing behavior. (§1 ticket 10.)
7. Add a one-liner to ticket 1a stating the empty hook body is intentional and gets filled by 6a. (§1 ticket 1a.)

1–3 fix carryover-incompletes; 4–7 fix new findings. None restructures or adds tickets.
