# Session handoff — 2026-05-13 (evening, revised)

**Top priority remains Phase 1 (agent-push-to-main), even in Andrew-present sessions.** Detail in the TOP PRIORITY block below. This priority was briefly displaced in an earlier draft of this handoff by `act-f048` (the nested-repo / coordination-plane reframe filed this session). On reflection it's the wrong call: `act-f048` *amplifies* Phase 1 rather than substituting for it, because the nested-repo design adds a second push (code remote + act remote) per close. Until Phase 1 lands, the doubling makes the day-to-day worse, not better. So Phase 1 stays the priority track until it's solved.

`act-f048` is filed (p1, supersedes `act-8d67`), with the push-fanout question added as an explicit accept criterion — the design must either depend on Phase 1 or spec a policy that avoids doubling (candidates: act remote is no-remote by default for solo work; `act sync` is per-session not per-close; push via `.git/hooks/post-commit` in the act repo, outside agent tool surface). Reconciliation walkthrough (the original "interactive design priority") still queued for an Andrew-present session, but should slot in *after* or *alongside* Phase 1, not ahead of it. Worked examples and design conversation captured in the "Reconciliation walkthrough" section below.

Two earlier filings from this session still stand: **`act-6c73`** (inbox-triage commit-noise bug in this repo) and **`act-5704`** (history-rewrite + wrapper chore in inbox-triage). Both partially obsoleted by `act-f048` — if the nested-repo design lands, the noise problem largely dissolves and act-6c73's investigation territory (amend-on-write vs explicit-batch vs do-nothing) becomes moot. Don't close them yet; that's part of the act-f048 acceptance criteria.

## TOP PRIORITY for next session — still agent-push-to-main

Unchanged from the 2026-05-13 (afternoon) handoff. Re-stated here so this handoff is self-contained.

Agents running an act loop must be able to push to main without operator intervention. Today's afternoon session demonstrated the failure live; the Claude Code auto-mode classifier blocks `act close --push` mid-session, forcing Andrew to push manually. That's incompatible with the agent-driven loop premise. Two deliverables:

1. **Land the structural fix** — execute "Do now" item 4 from `docs/orchestration-design.md`: update three skill sections (canonical-loop step 7, worktree `--push` trap reframing, auto-mode caveat reduced cost). This makes Mode B workers cleanly avoid the classifier.
2. **Solve the Mode A solo case.** The structural fix doesn't help here. Investigation territory; unverified options from the afternoon handoff:
   - Classifier opt-outs beyond `settings.json` permissions (settings.json proven insufficient — see sift/ finding in predecessor handoff).
   - Whether invoking act via its MCP server (`act_next`/`act_finish`) bypasses Bash-tool-level classifier gating.
   - Whether pushes can happen outside the agent's tool-use surface via an act post-close hook running `git push` as a subprocess outside Claude Code's permission layer.
   - Whether `--dangerously-skip-permissions` is the only blanket answer, and the smallest scope it can be applied to.
   - Whether the classifier can be informed in-context that a push is loop-authorized via commit-marker / message pattern / session declaration.
3. **Validate with a real loop.** Run an actual `/act:loop` against sift's existing backlog (10 seeded issues, PR #14 still open) or a fresh test project. Zero operator intervention is the bar.

`act-b90e` (version-control the act skill) should probably be promoted to p1 and done in the same pass — whatever skill changes ship from item 1 are non-trivial and warrant tracking.

## Reconciliation walkthrough (act-f048, next interactive-design step)

The load-bearing claim in `act-f048` is "code commits are canonical, act state is overlay" — which only works if `act doctor` can robustly reconstruct act state from code-commit markers. Before any implementation starts, walk worked examples for these five categories with Andrew and settle deterministic rules for each:

1. **Marker-without-issue.** Code commit ends in `(act-XXXX)` but no issue with that id exists in the act state. Cases to discuss: create op was written but never synced; agent typo in marker; cross-repo reference to an issue tracked in a different act state; stale marker from a deleted issue.
2. **Issue-without-marker.** Act state has an issue (claimed or closed) but no code commit in any paired repo carries the marker. Cases: still in progress (claim exists, work not yet committed); tracking-only issue that legitimately produces no code; lost work commit (machine died before push); wrong-claim that was never closed.
3. **Multi-marker.** Multiple code commits carry the same `(act-XXXX)` marker. Legal today (multi-commit work on one issue). Reconciliation rule: pick which commit "counts" for the close moment, or accept all and let the issue's `commit_marker_history` enumerate them.
4. **Unknown-id reference.** Marker references an id that's not a known issue *and* not a typo'd prefix of any known issue. Could be a marker pointing at a different act state's id space (multi-state operator) or pure garbage. Rule: log and skip, never auto-create.
5. **Claim-newer-than-commit.** Act state has a claim with HLC strictly after the latest code commit on the relevant branch. Either a legitimate race (claim happened, work in progress) or an orphaned claim (claim happened, agent died). Rule probably involves a stale-claim timeout — note overlap with `act-b5f8`.

For each, what we want out of the conversation is: (a) the exact doctor action (auto-fix, surface, log, ignore), (b) the agent-facing contract (when an agent observes this state, what does it do), (c) idempotency on repeated reconcile runs.

Output of this walkthrough lands in a new design note (probably `docs/coordination-plane-design.md`) and feeds the implementation issue list for `act-f048`.

## Four-phase plan beyond Phase 1

Established in conversation in the earlier 2026-05-13 session. `act-f048` substantially reshapes Phase 2 (and possibly more); revisit before doing Phase 2 work.

- **Phase 2 — noise-reduction synthesis.** Originally: consolidate `act-6c73` + `act-208e` + `act-dfa5` into one design pass with updated `docs/commit-noise-design.md`. **Substantially obviated by `act-f048`** — if act ops aren't in the code repo's git log to begin with, most of the noise problem dissolves. Don't start Phase 2 until `act-f048`'s design lands; the right work shape may turn out to be "close these three as obsoleted" rather than "synthesize them."
- **Phase 3 — CLI polish + data-model bugs.** Parallelizable across sub-agents because the items are disjoint — though per CLAUDE.md, watch for `cmd/act/internal/cli` merge conflicts. Items: act-4b45, act-7ecd, act-3c89, act-b891, act-982a, act-56a0, act-f800 (CLI polish); act-8c78, act-b7ad, act-492e, act-7574 (deeper bugs). Unaffected by `act-f048`.
- **Phase 4 — sharing/adoption (gated on Phase 1 working).** act-2204 (publish), act-e6a5 (brew/curl), act-8416 (Cowork), act-4fe6 (CC Web). Should not happen until the loop actually runs autonomously, or we'd onboard others into a known-broken thing. The nested-repo design from `act-f048` also affects onboarding ergonomics — worth folding the design in before publishing.

The longer design tasks (`act-d264` branch-discovery, `act-b5f8` stale-claim recovery) slot in around Phase 2/3 as Andrew-availability permits. `act-8d67` is superseded by `act-f048` and should be closed once `act-f048` is accepted.

## What happened this session

Four things, in order:

1. **Reviewed inbox-triage commit-log noise data.** Another Claude session evaluated inbox-triage's git log at Andrew's request: 69 of 99 commits (~70%) are pure act-op metadata. Dominant ops are NOT close — they're create (32), close (10), tombstone (6), add_dep (6), add_accept (6, with 5 consecutive on a single ticket), claim (5), update_field/reopen (3). Diagnosis: act's existing close-time bundling assumes claim → work → close, but the actual noise is from metadata-only ops that have no work commit to fold into — pre-claim grooming runs (create + add_dep + add_accept) and same-field rewrite bursts.

2. **Filed `act-6c73`** (p2 bug) in this repo: "handle pure-metadata act-op runs that close-time bundling can't fold (inbox-triage downstream data)." Includes the full inbox-triage stats, the two structural patterns close-time bundling can't help with (pre-claim runs + same-field bursts), and proposed-scope-for-investigation (amend-on-write vs explicit-batch-mode vs do-nothing). Linked as `relates` to act-208e, act-dfa5, act-6018.

3. **Filed `act-5704`** in `~/Workspace/inbox-triage`'s own backlog: "reduce commit-log noise from act-op runs (local cleanup pending core fix in act-6c73)." Two parts: one-time history rewrite (inbox-triage has no git remote, fully safe to squash consecutive metadata-op runs); going-forward wrapper script under `.claude/` that amends instead of new-commit when HEAD is a same-session metadata-only act-op. Closes when act-6c73 lands proper core support. This means the noise problem is tracked where it can actually be fixed today, without waiting for act core changes.

4. **Architectural design conversation produced `act-f048`** (p1 task, supersedes `act-8d67`). Started from "how do we reduce commit noise" and converged on "act shouldn't be in the code repo at all." Progression: side-ref invisibility (rejected — protection is policy not ACL) → separate nested repo with private remote (kept — structural ACL boundary, code repo stays clean of act noise, fork+PR story preserved) → coordination-plane reframe (act state's scope is the agent work, not bound to a code repo; can span repos or live anywhere; operator decides). Two consequential ideas landed alongside: (a) **code is canonical, act is overlay** — work commits with `(act-XXXX)` markers are the durable record, act state is best-effort coordination, `act doctor` reconciles act state from code markers; (b) **act replaces markdown task tracking** — fitness scope is broad, the default for non-trivial agent work, not a niche multi-agent tool. Implementation delta turns out to be small (init/gitignore, write-path target, `act sync`, doctor reconciler). The reconciliation contract is the next concrete design step — see "Reconciliation walkthrough" above.

5. **Push-fanout caught late.** Near the end of the conversation, Andrew flagged that the nested-repo design adds a second push per close (code remote + act remote), doubling Phase 1's surface. This was a real miss in the design framing — the architectural elegance of the coordination-plane reframe distracted from operational impact. Added as an explicit accept criterion to `act-f048`: the design must address push-fanout, either via dependency on Phase 1 or via a per-design policy that avoids doubling (no-remote-by-default for solo work, per-session sync rather than per-close, push via post-commit hook in the act repo outside agent tool surface). Until that's resolved, `act-f048` makes the day-to-day worse not better, hence Phase 1 stays top priority.

## Why we did NOT kick off Phase 1 work tonight

Andrew was at end-of-day. Phase 1 needs investigation + verification + a real loop run; not fire-and-forget. The history-rewrite + wrapper in inbox-triage (act-5704) is the one near-term cleanup we identified, but it touches inbox-triage's history and the wrapper design has edge cases (pushed-status detection, session identity, concurrent worktrees) — also wanting Andrew awake. Both queued.

## Key artifacts produced

- `~/Workspace/act/.act/ops/act-6c73/` — new bug, links to act-208e/dfa5/6018, includes inbox-triage stats.
- `~/Workspace/inbox-triage/.act/ops/act-5704/` — new chore in inbox-triage's backlog covering both the rewrite and the wrapper.
- `~/Workspace/act/.act/ops/act-f048/` — new p1 design task, supersedes `act-8d67`, relates to `act-6c73`, `act-6018`, `act-208e`, `act-dfa5`. Full reframe captured in the create op's description.
- This handoff (the previous afternoon handoff is in git history; its content is rolled forward into the TOP PRIORITY block above).

## Backlog state

Run `act ready` in this repo for current ordering. Nothing this session changed Phase 1 priority. The afternoon handoff's "still open from predecessor sessions" block remains accurate. New this evening: `act-6c73` (p2, this repo).

## What to look at first when resuming

1. **Phase 1, agent-push-to-main — top priority regardless of session shape.** Re-read the TOP PRIORITY block above. The earlier framing in this handoff that put `act-f048` first was wrong; act-f048 *amplifies* Phase 1 (doubles the push surface), so Phase 1 needs to land before act-f048's design is operationally meaningful.
2. **Sub-task: promote `act-b90e` to p1** and pair it with Phase 1 so the skill changes from "Do now item 4" are tracked.
3. **If Phase 1 lands and Andrew is engaging: `act-f048` reconciliation walkthrough.** Work through the five categories in "Reconciliation walkthrough" above with worked examples. Output goes to a new design note. Andrew explicitly wants this interactive.
4. **Phase 2 (noise-reduction synthesis) is on hold** pending `act-f048`'s design landing — the nested-repo reframe likely obviates most of Phase 2's premise. Don't start Phase 2 work without revisiting that judgment.
5. `act-2204` (publish + release tag) is still the sharing gate. Re-decide once Phase 1 validates *and* `act-f048` direction settles (onboarding ergonomics differ under the nested-repo model).
6. **Review and merge sift PR #14** when the skill update lands — sift is the alpha-trial project for the new framing.

## Cross-references

- Afternoon predecessor handoff: in git history (`docs/session-handoff.md` at HEAD~N).
- Orchestration design: `docs/orchestration-design.md` (Do-now item 4 is the structural fix for Phase 1).
- Commit-noise design note: `docs/commit-noise-design.md` (predates today's inbox-triage data; Phase 2 should refresh it).
- Global act skill (target of Phase 1 changes): `~/.claude/skills/act/SKILL.md`
- inbox-triage's own act backlog: `~/Workspace/inbox-triage/.act/`
- sift/: `~/Workspace/sift/` (PR #14 open, awaiting review)

## Operational notes

- Work in the earlier 2026-05-13 sessions: 1 `act create` in this repo (act-6c73), 3 `act dep add` ops linking it, 1 `act create` in inbox-triage (act-5704). All auto-committed; this repo's commits pushed to origin/main; inbox-triage has no remote.
- Work in the evening conversation that produced this handoff revision: 1 `act create` in this repo (act-f048), 5 `act dep add` ops linking it (supersedes act-8d67; relates to act-6c73, act-6018, act-208e, act-dfa5), 1 `act update` adding the push-fanout accept criterion to act-f048. Auto-committed locally, not yet pushed at the time of this handoff write.
- No `act close` ops across either session — none of the work was a tracked issue.
