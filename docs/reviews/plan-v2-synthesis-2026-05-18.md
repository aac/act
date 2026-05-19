# Plan v2 review synthesis — phase 2 coordination plane

**Inputs:**
- `docs/reviews/plan-v2-cold-eye-2026-05-18.md` (verdict: `needs-iteration` — explicitly "barely")
- `docs/reviews/plan-v2-architect-2026-05-18.md` (verdict: `plan-ready` with five should-fix items folded at dispatch)

Both reviews ran with >70% confidence filters against plan v2 (`docs/coordination-plane-phase2-plan.md` at commit `ff364cc`) and brief v4 (commit `a7f1bd1`).

**Synthesis verdict: `PLAN-READY` with per-ticket prompt addenda applied at dispatch.** Reasoning in §4. The split-verdict from the two gates is a real call to make, but on inspection the cold-eye reviewer's seven remediations are uniformly tightenings rather than structural rework, and every one of them maps cleanly to a specific ticket's prompt — the same fold-at-dispatch mechanism the architect endorsed. The carryover from v1 is substantively clean (cold-eye: 10/13 fine, 3/13 fixed-but-incomplete, 0 unaddressed; architect: 12/13 closed, 1 fixed-but-incomplete).

---

## 1. Convergent findings (both reviewers)

Findings raised independently by both reviewers, with the v1 carryover-incomplete subset called out.

### CV1. Ticket 2's fixture-package public API is missing `PauseTransfer`.

Cold-eye §1 ticket 2 ("Add `PauseTransfer` to ticket 2's API list. Otherwise 2 closes 'done' and 7 blocks on a missing helper") and architect §1 ticket 2 / §5 item-2 (same observation framed as a "ready for import" verifiability gap). This is v1 carryover item C6/item-6, fixed-but-incomplete — ownership pinned, enumeration drift remains. Maps to ticket 2's prompt: add `PauseTransfer(...)` to the enumerated API surface alongside `NewBareRemote`, `AdvanceCommits`, `ConcurrentPush`, `InitShallow`. Architect also recommends ticket 2 ship a smoke `Test_API_Surface` in `internal/testfixtures/remote_test.go` — fold that in as the verification mechanism.

### CV2. Missing explicit dep edges that the plan's prose acknowledges but the graph doesn't draw.

Cold-eye §3 names three edges: `5 → 3b`, `5 → 6b`, `2 → 9`. The first two come from plan prose at line 447 ("3a → 5 → 3b → 6b in `internal/gitops/gitops.go`") — same-file rebase ordering. The third is the envelope-code `remote_unreachable` introduced in ticket 2 and consumed in ticket 9 case (g). Architect §1 ticket 7 names the same class of issue from a different angle: `7 → 2` (PauseTransfer test-code dep) is missing. Architect §1 ticket 8 names another: `8 → act-b77a80` (`harvest.go` doesn't exist in main yet). This is the v1 carryover item C3/S6 / architect item 12, "fixed-but-incomplete" in both reviews — the v2 plan added some edges and missed others. Mechanism: each missing edge maps to one ticket's prompt as a `blocked-by` line the kickoff worker will write when filing.

### CV3. Phase 1.5 hard-edge rationale text is placed at 1a but actually justifies an edge at 7 (and arguably also 8).

Cold-eye §1 ticket 7 / §3 / §7 row S6 and architect §1 ticket 8 / §7 item-12 both note that the v2 plan's line-30 rationale ("ticket 7 extends `--import-from`") explains an edge that belongs at ticket 7, not ticket 1a. The edge at 1a is still defensible (1a depends on Phase 1.5 transitively via 7), but the rationale is mis-placed and 8's transitive dep is undocumented. Maps to: ticket 7's prompt gets an explicit `blocked-by act-b77a80` edge added (or kept-via-1a + rationale clarified); ticket 8's prompt gets the same explicit edge.

---

## 2. Single-reviewer findings worth carrying forward

Each was raised by only one reviewer but is load-bearing on a second read.

### S1. 3a's files-touched manifest is materially overstated (architect §1 ticket 3a).

Architect inspected the codebase: `internal/cli/util.go` already centralizes commit-and-push via `WriteOpAndAutoCommit`/`WriteOpsAndAutoCommit` (with an existing `opts.Push` flag), and of the six write subcommands, only `close.go` has a non-helper `gops.Commit()` call. Realistic surface is `util.go` + `close.go` + `gitops.go`, not all seven files. The AC ("all six write subcommands invoke `PushWithRetry` exactly once per successful commit") is correct; only the files-touched line is wrong. Cold-eye missed this because they didn't grep the codebase. **Carry forward** as a ticket-3a prompt addendum: re-read the files-touched manifest and narrow before dispatch.

### S2. 3a's `ACT_TEST_FAIL_PUSH_AFTER` documentation location is unspecified (cold-eye §1 ticket 3a / §2).

Variable named in the plan but doc location isn't pinned — compare 3b's hook (`internal/gitops/gitops.go` next to the hook, line 160). **Carry forward** as a one-line prompt addendum on ticket 3a: pin the location as a comment in `internal/gitops/gitops.go` adjacent to the helper.

### S3. 3b's `.act/.pending-pushes` schema is unpinned (cold-eye §1 ticket 3b / §2; architect §1 ticket 3b "consider").

Plan pins `.slow-writes` schema field-by-field; `.pending-pushes` says "the local commit's SHA" but doesn't pin the file format (JSON-lines? one SHA per line?). Lower stakes than `.slow-writes` because the same ticket owns the only consumer, but doctor will inspect this eventually. **Carry forward** as a ticket-3b prompt addendum: pin `.pending-pushes` schema in the same shape as `.slow-writes`.

### S4. Ticket 10's CI-machine symlink-missing behavior is unspecified (cold-eye §1 ticket 10 / §2).

The act-side test for the orchestrate.md symlink reads via `os.Readlink`, but on non-Andrew CI runners the target won't exist. Plan should pin: skip on `fs.ErrNotExist`, or gate to Andrew-only runners. **Carry forward** as a ticket-10 prompt addendum.

### S5. Ticket 9 case (h) needs `--no-fetch` suppression (architect §3, "fail-soft sync × doctor case (h)").

If `--no-fetch` is passed and case (h) requires fetched upstream state, case (h) should suppress emission entirely (not false-positive against stale cache). Cold-eye missed this because they didn't trace the fail-soft × doctor interaction. **Carry forward** as a ticket-9 prompt addendum: add to AC.

### S6. Ticket 5's `--no-cache` alias needs its own `TestDocClaim_*` entry (architect §1 ticket 5 / §4).

Plan says `--fresh` and `--no-cache` are aliases with identical behavior — that's a doc claim and needs a registry entry asserting both flags appear in `--help` and dispatch identically. **Carry forward** as a ticket-5 prompt addendum: add the second `TestDocClaim_*` entry.

### S7. Ticket 1a's empty post-receive hook intermediate state needs a comment for cold pickups (cold-eye §1 ticket 1a / §5 negative).

1a installs an empty `post-receive`; 6a fills the body. A cold agent picking up 1a won't know the empty file is intentional. Also: 1a-on-main without 6a leaves workers pushing into a no-op state. **Carry forward** as two ticket-prompt addenda: 1a (add a one-line comment that hook body is intentionally empty until 6a lands), and an orchestrator-level note in the kickoff ticket (land 1a and 6a in the same release window).

### S8. Ticket 7's interference-test definition is under-specified (cold-eye §1 ticket 7).

AC for "concurrent bootstraps to different target paths succeed without interfering" doesn't define what interference is being checked. **Carry forward** as a ticket-7 prompt addendum: pin the verification ("N parallel bootstraps, each followed by `act ready`, all returning the same state").

### S9. Ticket 8's orchestrator-path resolution mechanism is under-specified (cold-eye §1 ticket 8).

How does the orchestrator know "its own canonical `.act/.git` path" for the origin-match? Plausibly from CWD of the harvest invocation. **Carry forward** as a one-line ticket-8 prompt addendum.

---

## 3. Single-reviewer findings explicitly dropped

These were flagged but don't earn the carry.

- **Cold-eye §2 over-spec on ticket 2's typed-error enumeration.** "Plan should commit to typed errors and let impl name them." True observation, but the v2 plan's specificity here is harmless and the v1 architect's whole pitch on this ticket was that it needed pinning. Drop as anti-finding.
- **Cold-eye §2 over-spec on ticket 1a's seven config-key defaults.** Same shape: the pinning is the asset, not the liability. Drop.
- **Architect §2 wording fix on "after 3a lands: 5, 3b, 6a in parallel" vs. the same-file sequencing.** True but the prose two paragraphs earlier already says the right thing; the orchestrator reads both. Drop as anti-finding (the dep-graph fixes in CV2 cover the load-bearing case).
- **Architect §3 "doctor validates `act.role` vs topology consistency"** as a follow-up. Already flagged "consider, not Phase 2." Drop from carryover.
- **Architect §3 "per-clone `.act/.slow-writes` semantics."** Implementer-readable from the design; not a plan-stage finding. Drop.
- **Architect §3 ticket 9 case (h) "stderr literal vs. configurable threshold".** Trivial wording fix the implementer will catch. Drop as taste-level.
- **Cold-eye §1 ticket 6a "fsnotify-or-equivalent dep pre-clearance".** The dep is implicit-but-fine; project already uses Go modules. Drop.

---

## 4. Verdict and reasoning

**`plan-ready` with prompt-level patches.** Reasoning:

The architect explicitly recommended plan-ready-with-patches; the cold-eye recommended needs-iteration-barely. The split is real but on inspection the cold-eye reviewer's own §7 row analysis (10/13 v1 items addressed-and-fine, 3 fixed-but-incomplete, 0 unaddressed) and §6 closing characterization ("none restructures or adds tickets") describe a plan that is structurally complete and substantively correct. The seven remediations they list are all per-ticket tightenings, and each maps cleanly to a specific ticket's worker prompt.

The decision rule from §verdict on the canonical arc ("plan-ready-with-prompt-level-patches is on the table IF the small findings can be expressed cleanly as per-ticket prompt addenda"): all findings here meet that bar. Specifically:

- **Dep-edge findings (CV2):** 5→3b, 5→6b, 2→9, 7→2, 8→act-b77a80 all map to `blocked-by` lines on specific tickets. The kickoff ticket's worker writes these when filing the 13 tickets; no plan-doc edit needed.
- **API-enumeration finding (CV1):** PauseTransfer maps to a one-line addendum on ticket 2's prompt — "include PauseTransfer in the documented public API; add a `Test_API_Surface` smoke compile-test."
- **Rationale-placement finding (CV3):** Either rewrite line 30 or move the edge; both are trivial. Either path stays inside v2.
- **All §2 single-reviewer findings (S1–S9):** Each is a one-line addendum to exactly one ticket.

Going to v3 would mean another 1.5 review cycles (iteration + cold-eye + architect + synthesis) to pin five `blocked-by` edges, one API enumeration line, and seven one-line ticket clarifications. That cost is not justified by the marginal-risk-reduction those changes deliver — none of them touches the plan's structural invariants (the 13-ticket decomposition, the phase boundaries, the test/discipline architecture, the cross-cutting constraint). The architect's framing — "should-fix the orchestrator can fold into ticket bundles at dispatch, not gate iteration on" — is the right read of the actual finding set.

One additional argument for plan-ready: the carryover analysis. Both reviewers found v2 substantively closed v1's must-fix list. Cold-eye 10/13 fine + 3/13 incomplete. Architect 12/13 fine + 1/13 incomplete. The remaining incompleteness is exactly the prompt-patch surface above — the same incompleteness another iteration cycle would address one at a time. Closing it via prompt-patches at dispatch is the same effect, faster.

What I'm explicitly NOT doing: dispatching with the cold-eye finding C1 (the three dep-edge fixes) unaddressed. The plan's value proposition is "executing agent picks up any ticket cold"; missing dep edges that the prose acknowledges defeats that. The mechanism in §5 binds the kickoff ticket to apply every edge correction before any implementation ticket gets filed.

---

## 5. Mechanism: per-ticket prompt addenda

The kickoff ticket's worker reads plan v2's per-ticket subsections, files 13 act tickets, and applies the addenda below before each `act create`. Each addendum maps to exactly one filed ticket.

| Ticket | Addendum |
|---|---|
| **1a** | Files-touched comment: "the post-receive hook body is intentionally empty until ticket 6a lands; do not back-fill in 1a's scope." Orchestrator-level note in the kickoff: land 1a and 6a in the same release window. |
| **1b** | (Implicit `blocked-by 2` already covered by the canonical filing — fixture import.) |
| **2** | Add `PauseTransfer(target time.Duration)` to the documented public API surface alongside the existing four. Add `internal/testfixtures/remote_test.go` with a `Test_API_Surface` no-op compile-test against the public API. |
| **3a** | Files-touched: narrow to `internal/gitops/gitops.go` + `internal/cli/util.go` + `internal/cli/close.go` + (signature changes to Push helper if needed). Re-verify before dispatch. Pin `ACT_TEST_FAIL_PUSH_AFTER` documentation as a code comment in `internal/gitops/gitops.go` adjacent to the helper. |
| **3b** | Pin `.act/.pending-pushes` schema in the same shape as `.slow-writes` (JSON-lines, one record per line with named fields). Explicit `blocked-by 5` edge added (same-file rebase ordering on `internal/gitops/gitops.go`). |
| **5** | Add second `TestDocClaim_*` entry for the `--fresh` / `--no-cache` alias equivalence (both flags in `--help`, identical dispatch behavior). |
| **6a** | (No addendum beyond canonical filing; 1a/6a release-window pairing tracked at orchestrator level.) |
| **6b** | Explicit `blocked-by 5` edge added (same-file rebase ordering). |
| **7** | Explicit `blocked-by 2` edge added (test-code dep on `PauseTransfer`). Tighten interference-test AC: "N parallel bootstraps to disjoint target paths, each followed by `act ready`, all return the same state and exit 0." |
| **8** | Explicit `blocked-by act-b77a80` edge added (extends `internal/cli/harvest.go` which Phase 1.5 ships). Pin orchestrator-path resolution: "orchestrator's canonical `.act/.git` path is resolved from the CWD of the `act harvest` invocation." |
| **9** | Explicit `blocked-by 2` edge added (consumes `remote_unreachable` envelope code). Add to AC: "`act doctor --no-fetch` suppresses case (h) emission entirely (case (h) detection requires a successful upstream fetch and cannot run against stale cache)." |
| **10** | Pin CI-runner symlink-missing behavior: the act-side test skips on `os.IsNotExist(err)` from the `os.Readlink` call, with a `t.Logf` noting the skip-reason. Production behavior unaffected; only the test path skips. |
| **11** | (No addendum.) |

Rationale: all twelve addenda fit on one screen of ticket-creation prompt; the kickoff worker applies them deterministically.

---

## 6. Filed followups

- **`act-56e6c6`** — kickoff: `Phase 2 orchestration kickoff: file 13 implementation tickets from plan v2`. Description embeds the §5 addenda table verbatim. Worker reads plan v2 per-ticket subsections, files 13 act tickets with appropriate scopes/ACs/deps from the plan, applies the per-ticket addenda from §5 as it files each one, and wires `blocked-by` edges per §5.
- **`act-abbf4b`** — E2E dogfood: `Phase 2 E2E dogfood: real two-machine round-trip after implementation tickets land`. Currently `blocked-by act-56e6c6`; the kickoff worker will add additional `blocked-by` edges to each of the 13 implementation tickets it files.

No v3 iteration, no v3 review tickets — the verdict is plan-ready.

**Word count:** ~2270.
