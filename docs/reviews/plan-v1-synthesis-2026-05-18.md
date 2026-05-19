# Plan v1 review synthesis — phase 2 coordination plane

**Inputs:** `docs/reviews/plan-v1-cold-eye-2026-05-18.md` (verdict: `needs-iteration`), `docs/reviews/plan-v1-architect-2026-05-18.md` (verdict: `needs-iteration`). Both reviews ran with >70% confidence filters against plan v1 (`docs/coordination-plane-phase2-plan.md` at commit `d07b4f5`) and brief v4 (commit `a7f1bd1`).

**Synthesis verdict:** `NEEDS-ITERATION`. Both reviewers converge on the same root cause: the plan smuggles real decisions ("open implementation questions") past the plan gate, and a handful of tickets are mis-sized in ways that cascade into the dep graph. None of the remediations require redesigning anything in the brief; they are plan-level corrections.

---

## 1. Convergent findings (both reviewers flagged)

These are the must-fix list. Highest confidence because two independent passes raised each one.

### C1. Orchestrator detection (open question #4) must be decided in the plan, not punted to ticket 6.

Cold-eye §1 (ticket 6) and §4: "the cleaner answer (`act.role=orchestrator` config key set by ticket 1) … doesn't commit. The proposed heuristic (`origin is .` or path under same filesystem root) breaks under worktrees, symlinks, bind-mounts." Architect §1, §3, §6: "if the answer is config key `act.role=orchestrator`, that key belongs in ticket 1a's schema. Decide in the plan, not at dispatch time." Failure modes named by the architect: symlinks, `~/.local/share/act/<project>` extracted state, NFS-mounted orchestrator. Fix: commit to the `act.role` config key, add it to ticket 1's config schema, have ticket 6 read it. Default `act.role=orchestrator` if unset and origin looks local, but treat the heuristic as a fallback inside a defined key, not as the primary mechanism.

### C2. Open question #1 (slow-write log schema) must be decided in the plan.

Cold-eye §4: "Schema affects ticket 3 (writer) and ticket 9 (reader). Pin it now." Architect §6: "OQ #1 — decide now. `{timestamp, op_id, duration_ms, op_type}` is fine." Both reviewers explicitly accept the strawman schema in the plan body as sufficient. Fix: promote `{timestamp, op_id, duration_ms, op_type}` from "open question" to ticket-3 file-format spec, and update ticket 9's case-(g) acceptance to assert against that schema.

### C3. Ticket 3 is over-scoped and the dep graph mis-states ticket 3 ↔ ticket 5 parallelism.

Cold-eye §3 (impl dep #4): "Tickets 3 and 5 both touch `internal/gitops/gitops.go`… The plan says they can run in parallel after ticket 2. They can't — both edit the same file." Architect §1: "Ticket 3 is two tickets in a trenchcoat — and the plan admits it. Split 3a (wire `PushWithRetry` into every write path) + 3b (`--offline` + `.pending-pushes` + slow-write warning + `.slow-writes`)." The two reviewers attack from different angles (file-overlap dep edge vs. ticket-decomposition) but converge on the same fact: ticket 3 is too large and its overlap with adjacent work isn't represented. Fix: split ticket 3 into 3a (push-on-write integration) and 3b (offline flag + slow-write logging). Add explicit dep edge: ticket 5 rebases on ticket 3a (both edit `internal/gitops/gitops.go`).

### C4. Acceptance criteria don't name user-visible surfaces, weakest in tickets 3, 6, 9.

Cold-eye §1 (ticket 3): "Slow-write warning string isn't named. CLAUDE.md's discipline rule requires a `TestDocClaim_*` that greps for *something*. Pin the template." Cold-eye §1 (ticket 9): inherits from ticket 3 unspecified format. Architect §5 weakest-three: W1 (ticket 6: "verified the same way" punts), W2 (ticket 9: "suggests `act remote sync`" — no literal string named), W3 (ticket 3: "stderr slow-write warning" — text not named, schema not named, fault-injection hook not designed). Architect §5 general: "many acceptances are 'the test asserts X' without naming the user-visible surface X lives at." Fix: do an audit pass of all ACs and rewrite the ones that assert at internal-state level when the doc claim is user-visible (stderr, stdout, `--help`, file contents). For tickets 3/6/9 specifically: pin the exact stderr strings, the exact `.act/.slow-writes` schema, the exact doctor stderr text for case (h).

### C5. Ticket 4 is not a real ticket — drop it from the count.

Cold-eye §1 (ticket 4): "Not a real ticket — bundled. Drop the slot or rename to 'constraint reminder.' The dep graph references 1–11 but ticket 4 has no edges; a cold orchestrator might dispatch nothing." Architect §1: "Eleven tickets is actually ten + a doc-discipline constraint. Ticket 4 is 'bundled into 2 and 3, not a separate ticket.' Counting it inflates the surface. Renumber to 10." Fix: renumber. Ten tickets, with the error-envelope additions called out as a cross-cutting constraint in each affected ticket's bundle.

### C6. Fixture-remote infrastructure has no owner.

Cold-eye §3 (impl dep #3) and §1 (ticket 11): "Tickets 2, 6, 7, 11 share fixture-remote infrastructure (bare repo + `git daemon` or equivalent). Plan flags this in risks but doesn't make ticket 2 owner of the shared fixture. Without that, every later ticket reinvents." Cold-eye §1 (ticket 2): "settle on one fixture-remote shape (`git daemon` vs SSH-loopback vs bare-repo-on-filesystem) here and have later tickets reuse via a shared helper." Architect §4 alludes to the same problem at the test-mechanism level ("how does the test hit the right millisecond? Without a `git-shell`-shim or transport-layer injection, this test is a flake-magnet"). Fix: ticket 2 owns a shared `internal/testfixtures/remote.go` package; tickets 6/7/11 import. Pick one fixture-remote shape in ticket 2 and document it.

---

## 2. Single-reviewer findings worth carrying forward

These were raised by only one reviewer but look load-bearing on a second read.

### S1. Ticket 10's `~/.claude/commands/orchestrate.md` symlink trap (cold-eye §1, §5).

Cold-eye flags that ticket 10's "files touched" includes a symlink to Andrew's `claude-config` repo. A cold worker on a dispatched worktree commits the change "successfully" but it never reaches the act repo or the claude-config remote, because the symlink target is outside the worktree. **Carrying forward** because (a) it's a silent-failure mode — the agent thinks it shipped, doc never reaches Andrew; (b) the global CLAUDE.md explicitly warns about it ("after any edit, commit and push from the owning repo"); (c) the architect missed it entirely because they didn't read it as a worktree-isolation problem. Fix: ticket 10 names the symlink target explicitly and requires the cross-repo commit-and-push step.

### S2. Ticket 1 should split into 1a (foundation + hook skeleton) and 1b (upstream wiring) (architect §1, §2).

Architect's case: 1b (`act remote add-upstream` + `--force-public`) is only needed by ticket 6, and 6 is downstream of 1 anyway. Splitting unlocks 1b to parallelize with the rest of Phase 2 instead of gating it. **Carrying forward** because the parallelism gain is real and the split costs nothing — 1a is a clean foundation ticket, 1b a clean feature-add. Cold-eye didn't raise this because they read ticket 1 ticket-by-ticket without doing critical-path analysis.

### S3. Ticket 6 should split into 6a (subcommand + hook) and 6b (orchestrator-write trigger) (architect §2).

Same logic as S2. 6a has no dep on ticket 3; only 6b does. Splitting opens 6a to run alongside 2/3/5/7. **Carrying forward** for the same reasons.

### S4. `act remote disable` doesn't remove the post-receive hook file (architect §3).

Architect: "post-receive hook is a file, not git-config. `act remote disable` unsets config but doesn't necessarily remove `.act/.git/hooks/post-receive`. Stale hooks survive a disable/re-enable cycle." **Carrying forward** because rollback is a stated brief property; if disable doesn't actually disable, the brief lies. Fix: ticket 1's idempotent-disable acceptance must cover the hook file.

### S5. Shallow-clone × push-contention interaction is untested (architect §3).

Architect: under busy orchestrator, a worker can hit contention 5 times in a row, each retry rebasing shallow state, exhausting the cap before the single `--unshallow` round fires. **Carrying forward** because it's a real correctness hole, not a sizing complaint. Cold-eye didn't run this stress because they didn't trace the interaction. Fix: add a test case to ticket 11 (or ticket 2) for shallow + repeated contention + exhaustion.

### S6. Phase 1.5 dep (`act-b77a80`) should be a hard dep edge, not a risk-list bullet (architect §2).

Architect: "Make it a hard dep: ticket 1a depends on `act-b77a80`. Otherwise an orchestrator dispatching ticket 1 cold won't notice." **Carrying forward** because the orchestrator dispatches off the dep graph; if it's not in the graph, it won't be respected.

### S7. `gitops.PushWithRetry` factoring decision (architect §2).

Architect: "Pick one in the plan: extract `gitops.FetchAndRebase` and have both 3 and 5 consume it. Otherwise ticket 5 either re-implements rebase recovery (drift risk) or serializes behind 2." **Carrying forward** because it's a real factoring decision that the plan defers. Cold-eye partially addressed this in C3 (the file-overlap finding) but didn't articulate the helper-shape question.

---

## 3. Single-reviewer findings explicitly dropped

These were raised by one reviewer but look like preference or scope-creep on a second read.

- **Architect §2(iii): move ticket 7 earlier for dogfood signal.** True observation, but reordering dispatch is the orchestrator's call at runtime, not a plan-level decision. The plan's dep graph already permits it. Dropped as a plan-stage finding; will surface naturally in dispatch.
- **Architect §4: "two-machine round-trip is two clones on one host."** True, but the architect themselves concedes it's "defensible for dogfood-only." Adding a ±10min clock-skew test is gold-plating for a one-user system. Dropped.
- **Architect §4: ticket 11's "4 workers × 50 ops" is over-tested.** Cosmetic. CI budget concern is real but solvable with `-short`. Dropped.
- **Architect §4: ticket 8's idempotency-twice test is redundant.** Possibly true but trivial cost; not worth a plan-iteration cycle to remove one assertion. Dropped (the implementer can collapse it during ticket 8 work).
- **Architect §7: defer ticket 11 (E2E), 1b (upstream), 9-case-(h), 3b (offline).** This is the "ship 80% in 50%" cut. Useful framing but Andrew has already committed to the full Phase 2 in the brief v4 review cycle. Not a plan-readiness issue; it's a scope-reset proposal. Dropped — if scope changes, that's a brief-level decision.
- **Cold-eye §1 (ticket 2): "Exponential backoff capped at 1s lacks a base. Name one (e.g., 50ms doubling)."** Fine to pin, but small enough that ticket 2's implementer can settle it without plan iteration. Dropped (folded into the C4 audit pass).
- **Cold-eye §2: ticket 2 and ticket 5 duplicate brief text verbatim.** True observation, but the duplicate-source risk hasn't manifested — brief v4 is frozen until plan v2 ships. Dropped as a stylistic concern, not a correctness one.

---

## 4. Verdict

**`NEEDS-ITERATION`.** Both reviewers said so explicitly. The six convergent findings are real, must-fix, and resolvable inside the plan without touching the brief.

---

## 5. Iteration scope — consolidated remediation list for plan v2

The v2 plan (overwrites v1 at `docs/coordination-plane-phase2-plan.md`) must address each of the following. Reviewer attribution in brackets.

1. **Decide open question #4 (orchestrator detection).** Add `act.role` config key to ticket 1's config schema; ticket 6 reads it. Remove OQ #4 from the open-questions section. [C1: cold-eye §1, §4; architect §1, §3, §6]
2. **Decide open question #1 (slow-write log schema).** Pin `{timestamp, op_id, duration_ms, op_type}` in ticket 3's file-format spec. Ticket 9 case-(g) acceptance asserts against it. [C2: cold-eye §4; architect §6]
3. **Split ticket 3 into 3a (push-on-write integration) + 3b (offline + slow-write logging).** Add explicit dep edge: ticket 5 rebases on 3a (both edit `internal/gitops/gitops.go`). [C3: cold-eye §3; architect §1]
4. **Audit all acceptance criteria for user-visible-surface naming.** Specifically tighten W1 (ticket 6 sync verification mechanism), W2 (ticket 9 case-(h) literal stderr string), W3 (ticket 3 slow-write stderr text + schema + fault-injection hook). [C4: cold-eye §1; architect §5]
5. **Drop ticket 4; renumber to ten tickets.** Error-envelope additions live as a cross-cutting constraint in each affected ticket's bundle, not as a separate slot. [C5: cold-eye §1; architect §1]
6. **Make ticket 2 the owner of `internal/testfixtures/remote.go`.** Pick one fixture-remote shape (recommend bare-repo-on-filesystem; simplest). Tickets 6/7/11 import. [C6: cold-eye §3, §1; architect §4]
7. **Add ticket 10's symlink trap.** Name `~/.claude/commands/orchestrate.md`'s symlink target explicitly; require cross-repo commit-and-push in the ticket bundle. [S1: cold-eye §1]
8. **Split ticket 1 into 1a (foundation + config + `act remote enable/disable` + hook skeleton) and 1b (`act remote add-upstream` + `--force-public` + `upstream_public` error).** 1b parallelizes with Phase 2 work. [S2: architect §1]
9. **Split ticket 6 into 6a (subcommand + hook content) and 6b (orchestrator-write trigger).** 6a runs alongside 2/3a/5/7; only 6b waits on 3a. [S3: architect §2]
10. **Tighten ticket 1's idempotent-disable acceptance to cover hook-file removal.** [S4: architect §3]
11. **Add shallow + repeated-contention + exhaustion test case** (ticket 11 or ticket 2). [S5: architect §3]
12. **Promote Phase 1.5 dep to hard edge.** Add explicit `blocked-by act-b77a80` on ticket 1a. [S6: architect §2]
13. **Decide `gitops.PushWithRetry` factoring.** Extract `gitops.FetchAndRebase`; both ticket 3a and ticket 5 consume it. Pin the helper shape in ticket 2's design. [S7: architect §2]

Items 1–6 are the convergent must-fix list. Items 7–13 are the single-reviewer carries. All thirteen are in scope for v2.

**Out of scope for v2** (deferred or dropped): the dispatch-order reordering for ticket 7, the clock-skew test, CI-budget trimming on ticket 11, the ticket-8 idempotency dedup, the "ship 80% in 50%" scope cut, the ticket-2 backoff-base value, the brief-text-duplication style note.

**Net expectation for v2.** Plan grows from 11 tickets to 13 (10 originals minus ticket 4, plus 1a/1b, 3a/3b, 6a/6b splits = 13). Open-questions section shrinks from 4 entries to 1 (worker telemetry, correctly deferred to Phase 3). Dep graph gains four explicit edges. Acceptance criteria for tickets 3a/3b/6a/6b/9 get tightened to name user-visible surfaces. No design changes; all corrections are plan-level.
