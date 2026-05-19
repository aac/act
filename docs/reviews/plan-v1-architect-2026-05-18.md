# Phase 2 plan v1 — architect review

**Reviewer perspective:** senior staff engineer / architect, pre-implementation review.
**Plan under review:** `docs/coordination-plane-phase2-plan.md` (commit `d07b4f5`).
**Brief:** `docs/coordination-plane-phase2-design.md` v4 (commit `a7f1bd1`).
**Filter:** >70% confidence on findings. No taste-level nits.
**Verdict (TL;DR):** `needs-iteration` — three concrete decomposition/sequencing changes; the rest are smaller asks.

---

## 1. Decomposition assessment

**Eleven tickets is actually ten + a doc-discipline constraint.** Ticket 4 is "bundled into 2 and 3, not a separate ticket." Counting it inflates the surface. Renumber to 10, with test-discipline as a cross-cutting constraint living in each ticket's bundle.

**Ticket 1 is two tickets in a trenchcoat.** It bundles (a) six new config keys, (b) `act remote enable/disable`, (c) `act remote add-upstream` + public-URL refusal, (d) hook-skeleton installation. (a)+(b)+(d) are the foundation everything depends on. (c) is upstream-wiring only ticket 6 needs, and 6 is downstream of 1 anyway. Split:
- **1a:** config keys + `act remote enable/disable` + post-receive hook skeleton (empty body).
- **1b:** `act remote add-upstream` + `--force-public` + `upstream_public` error code.

1a is blocking; 1b parallelizes with anything in Phase 2.

**Ticket 3 is two tickets in a trenchcoat — and the plan admits it.** "Files touched: create/close/update/depadd/reopen/delete + offline.go ... do not run in parallel with any other write-path-touching ticket." That last clause is the tell. Split:
- **3a:** wire `PushWithRetry` into every write path. No new flag, no new state file.
- **3b:** `--offline` flag + `.pending-pushes` queue + slow-write warning + `.slow-writes` file.

3a is the high-blast-radius change. 3b is feature-add on top, more amenable to focused review. Slow-write logging is also a separate concept from offline queueing — they shouldn't share a commit.

**Tickets 8, 9, 10 are correctly sized.** Each focused on one surface. Ticket 11 sprawls — see §4.

**Ticket 6 buries the orchestrator-detection question.** Open question #4 ("how does `actGitOps` know it's the orchestrator?") ripples through ticket 6's write-trigger and ticket 8's harvest-skip. The plan defers it to ticket-6 implementation. Wrong: if the answer is config key `act.role=orchestrator`, that key belongs in ticket 1a's schema. Decide in the plan, not at dispatch time.

## 2. Sequencing and parallelism

**Claimed critical path: 1 → 2 → 3 → 6 → 9 → 10/11.** Six sequential bottlenecks gating ten tickets.

**(i) Ticket 2's helper shape is under-specified for ticket 5's use.** Ticket 5 invokes ticket 2's helper "for the rebase failure-handling path." If 5 uses `PushWithRetry`, 5 depends on 2 (loss of claimed parallelism). If 5 only uses 2's fetch/rebase sub-helper, that sub-helper must be factored at ticket-2 design time. Pick one in the plan: extract `gitops.FetchAndRebase` and have both 3 and 5 consume it. Otherwise ticket 5 either re-implements rebase recovery (drift risk) or serializes behind 2.

**(ii) Ticket 6's dependency on ticket 3 is overstated.** Ticket 6 bundles three things: the `act remote sync` subcommand (no dep on 3), the post-receive hook content (no dep on 3), and the orchestrator-write trigger (depends on 3). Split into 6a (subcommand + hook) and 6b (orchestrator-write trigger). 6a runs in parallel with 2/3/5/7; only 6b waits on 3.

**(iii) Ticket 7 (bootstrap-worker `--from-remote`) blocks dogfood validation.** Real dogfood signal — does `/orchestrate` work end-to-end? — needs 1+3+7. The plan reaches that at end-of-phase-2 wall-clock. Ticket 7 has no dep on 2/3 in the plan's own graph, only on 1. Move it earlier in the dispatch schedule so dogfood signal arrives earlier.

**(iv) Phase 1.5 dependency is a risk-list bullet, not a dep edge.** The plan says "verify Phase 1.5 lands before ticket 1." Make it a hard dep: ticket 1a depends on `act-b77a80`. Otherwise an orchestrator dispatching ticket 1 cold won't notice.

**Net:** with the 1a/1b, 3a/3b, 6a/6b splits and the helper-factoring decision, realistic parallelism opens to 4-5 simultaneous workers across phases 2-3, not the current 1-2.

## 3. Risk surface

**Highest-blast-radius unnamed risk: shallow-clone × push contention.** Ticket 7 uses `--depth 1`; the brief specifies `--unshallow` as a fallback that fires once per `PushWithRetry`. Under a busy orchestrator a worker can hit contention 5 times in a row, each retry rebasing shallow state, exhausting the retry cap before the single `--unshallow` round produces deep-enough history. Worker dies under load with no `act doctor` signal because no state was written. The "unshallow once" semantic survives into ticket 2's design but no test exercises shallow + contention + exhaustion in interaction.

**Where the plan is most likely to discover the brief got something wrong: orchestrator detection.** "Origin is `.` or a path under the same filesystem root as `.act/`" fails for symlinks, the `~/.local/share/act/<project>` extracted-state case, and orchestrator-on-NFS-mounted-from-worker. Open question #4 already flags this. Decide on `act.role` before ticket 1 ships.

**Worst-case rollback:** plan claims `act remote disable` reverses everything. True at the op level. *Not* true for ticket 10's `/orchestrate` doc edit — it persists after disable. Next dispatch tries to clone a remote that no longer exists. Either `act remote disable` refuses while `/orchestrate` is still on the Phase 2 path, or it reverts the doc, or the doc has a configurable cutover knob. None in the plan.

**Second unnamed risk: post-receive hook is a file, not git-config.** `act remote disable` unsets config but doesn't necessarily remove `.act/.git/hooks/post-receive`. Stale hooks survive a disable/re-enable cycle. Ticket 1's "idempotent disable" acceptance doesn't cover this.

## 4. Test strategy

**Under-tested: silent-rejection recovery (ticket 2).** "Fixture dirties the receiving tree mid-push" is hand-wavy — how does the test hit the right millisecond? Without a `git-shell`-shim or transport-layer injection, this test is a flake-magnet. Pick a deterministic mechanism in ticket 2's design.

**Under-tested: post-receive hook (ticket 6).** "Tail the sync-log and watch for entries" is polling with no upper bound. Under CI this becomes a 30-second timeout on the green path. Use `fsnotify` or pipe the hook to a known FD. The test-plan phrase "bounded waits and explicit notification" acknowledges this; implementation has to match.

**Under-tested: multi-machine semantics.** Ticket 11's "two-machine round-trip" is two clones on one host. Catches the protocol but not clock skew, network partition, SSH auth drift. The brief relies on HLC clock-skew bounds (§Clock skew); no ticket tests them. For dogfood-only this is defensible — but the plan should *say so* rather than silently underspec. Add one ±10min clock-skew determinism test.

**Possibly over-tested: ticket 8's idempotency.** Phase 1.5's existing suite covers no-op-on-empty-diff. The new "harvest twice — both no-op" case is the same test run twice; drop one.

**Possibly over-tested: ticket 11's "4 workers × 50 ops."** 200 ops dominates the 2-minute CI budget. 4 × 5 catches the same bug class at 1/10th the cost.

## 5. Acceptance criteria quality

Three weakest, ranked:

**(W1) Ticket 6: "Orchestrator's own `act create` on a remote-configured project produces both a local commit and a background sync invocation — verified the same way."** "Verified the same way" punts on what "the same way" means. The prior bullet uses log-tailing, which is unreliable (see §4). The acceptance reads as "we'll figure out the test mechanism later." Replace with: "after `act create` returns, a marker file written by the sync invocation is observable within 2 seconds via fsnotify or equivalent."

**(W2) Ticket 9: "`origin-upstream` 60 commits behind `origin` flags case (h) and suggests `act remote sync`."** "Suggests" is unverifiable without naming the user-visible string. The repo's documentation discipline (`CLAUDE.md`) requires user-visible claims to land with `TestDocClaim_*` entries. "Suggests" doesn't name the surface. Replace with the exact stderr line the doctor emits, e.g. "doctor stderr contains the literal substring `run 'act remote sync' to publish 60 local commits to origin-upstream`."

**(W3) Ticket 3: "A 2-second sleep injected into the commit path (test-only fault injection) produces a stderr slow-write warning and a `.act/.slow-writes` entry."** Doesn't name the exact stderr text, doesn't say what the entry's schema is (the plan's open question #1 admits the schema isn't decided), and "test-only fault injection" implies a hook point that doesn't yet exist in `actGitOps`. Three sub-decisions hidden in one acceptance criterion. The fault-injection hook should be its own deliverable, named in the file list, with a clear contract.

A general weakness across tickets: many acceptances are "the test asserts X" without naming the *user-visible surface* X lives at. `CLAUDE.md`'s discipline rule says assertions go at the user-visible boundary. Half of the plan's acceptances assert at internal-state level ("`.act/fold-checkpoint.json` does not survive — verified by reading it before and after"). That's fine for internal invariants, but it's not how the discipline rule asks the claim to be tested. Audit pass before kickoff.

## 6. Open questions vs. plan-decisions in disguise

**OQ #1 (slow-write schema)** — decide now. `{timestamp, op_id, duration_ms, op_type}` is fine. Ticket 3b's acceptance can't be unambiguous until the schema is fixed.

**OQ #4 (orchestrator detection)** — decide now. See §3. The config-key approach (`act.role=orchestrator`) is more robust; promote to a ticket-1a decision.

**Decisions in disguise:**

- **`act remote sync` returns exit 0 on failure (ticket 6).** UX call with consequences — interactive `act remote sync` looks successful when it isn't. Ship with `--strict` from day one and explicit doc.
- **Ticket 3's "do not run in parallel with any other write-path ticket" (risks list).** This is a process decision, not a risk. Means "ticket 3 stops the world for a full dispatch cycle." Decide whether to take the hit or factor (§1's 3a/3b split).

## 7. What you'd cut to ship 80% in 50%

Five tickets to cut, in priority order:

1. **Ticket 11 (E2E suite) — defer.** The phase tickets each carry their own integration tests. A separate E2E suite is reassurance, not insurance, for a single-user dogfood audience. Cut it and ship; if something breaks in dogfood, file the failure mode as a focused test ticket post-hoc. (Cost: less confidence at landing; benefit: ships 1-2 dispatch cycles sooner.)
2. **Ticket 1b (`act remote add-upstream`) — defer.** The GitHub upstream is for durability across machine loss. Single-user dogfood mostly runs on one machine; durability matters when (a) the machine actually dies or (b) the second machine is brought online. Defer until either signal appears. (Cost: no durability if Andrew's machine dies during the Phase 2 window; benefit: 1 ticket cut.)
3. **Ticket 9's case (h) (upstream drift detection) — defer with 1b.** If 1b doesn't ship, (h) is dead code. (Cost: zero, dependent on 1b.)
4. **Ticket 3b's `--offline` flag + `.pending-pushes` — defer.** Offline mode is a network-resilience feature. The harvest fallback already exists for the local-commit-but-push-failed case. `--offline` is operator convenience, not correctness. (Cost: agents working without network must manually retry; benefit: one of the most contested surfaces is deferred.)
5. **Ticket 8's idempotency-twice test — drop one duplicate as called out in §4.** (Trivial cost; trivial benefit.)

**What ships in the cut version:** push-on-write with retry, read TTL cache, dispatch-mode bypass, bootstrap-worker from remote, harvest narrowed to fallback, doctor cases (a')/(c')/(f)/(g), orchestrate doc updated. That's the load-bearing 80%.

**What does NOT ship:** GitHub durability, upstream drift detection, offline write queueing, separate E2E suite.

**The trade-off:** the cut version is the minimum viable Phase 2 for the dogfood loop. Real users (Andrew) will hit `--offline` need within weeks; defer-but-don't-delete is fine.

## What's working well (do not break these)

- **The dependency graph is explicit and approximately right.** The shape (1 → 2/5/7 → 3/6 → 8 → 9 → 10/11) is the natural shape of this work. Subsequent iteration should refine within it, not replace it.
- **Test-discipline plumbing is threaded through every ticket's file list and test plan.** `TestDocClaim_*` registry entries are named for the new claims. This is the right hygiene for a repo whose two largest prior bugs were doc-drift; the plan respects it.
- **Phase boundaries match real serialization constraints.** Ticket 1's foundation must precede everything; ticket 3's write-path edits must precede ticket 6's sync trigger. The plan doesn't pretend these are parallel — that honesty matters more than the cosmetic parallelism the splits in §1 would unlock.

---

## Verdict

`needs-iteration`.

Smallest set of changes to reach `plan-ready`:

1. Renumber ticket 4 out of the plan (it's a constraint, not a ticket). 10 tickets, not 11.
2. Split ticket 1 → 1a (foundation) + 1b (upstream wiring); split ticket 3 → 3a (push-on-write) + 3b (offline + slow-write logging); split ticket 6 → 6a (subcommand + hook) + 6b (orchestrator-write trigger).
3. Decide open questions #1 (slow-write schema) and #4 (orchestrator detection — recommend `act.role` config key) in this plan, not at ticket-implementation time.
4. Add dep edge: ticket 1a depends on `act-b77a80` (Phase 1.5 umbrella). Make it a hard gate, not a risk.
5. Tighten three weakest acceptances (W1, W2, W3 above) — name the exact user-visible surface each test asserts on.
6. Decide `gitops.PushWithRetry` factoring (extract `FetchAndRebase` for ticket 5's use) at ticket-2 design time, in the plan.
7. Add `act remote disable` semantics: remove the post-receive hook file, not just unset config; specify rollback story for the `/orchestrate` doc edit in ticket 10.

With those seven changes, the plan is `plan-ready`. None require rethinking the design; all are plan-level refinements.
