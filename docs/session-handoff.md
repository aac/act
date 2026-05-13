# Session handoff — 2026-05-13

This session was a design-refinement pass: processed the sift/ alpha-trial output, did a long-form design discussion with Andrew about how the canonical loop interacts with the Claude Code auto-mode classifier, extended `docs/orchestration-design.md` with three new sections, ran a multi-modal review pass on those additions, and filed three derivatives. Predecessor handoff (2026-05-11) is in git history; its top-of-queue items are still open and rolled forward below.

> **Quick read:** sift/ ran an earlier act prompt and reported back two things: (1) it produced a clean 10-issue seeded backlog with PR #14 open, and (2) the act skill's "auto-mode caveat" section is wrong — Claude Code's auto-mode classifier rejects worker pushes to main regardless of the settings.json allowlist, because the classifier and the permissions system are independent gates. That triggered a design discussion: the cleanest fix is to stop having workers push to main at all and route integration through an *orchestrator role* (Claude Code with sub-agents is the reference impl available today; gas town on beads is another candidate; a future built-in could be a third). `docs/orchestration-design.md` got three new sections capturing this — Orchestrator implementations, push asymmetry in the worker kernel, and log noise as a control surface. Reviewed via three parallel subagents (`act-334e`); inline-resolved seven findings and filed three derivatives (`act-d264` branch-discovery, `act-b5f8` stale-claim recovery, `act-208e` orchestrator-scoped bundling). The session itself produced live evidence the design is correct — the auto-mode classifier blocked a push mid-session, exactly the failure mode the proposed skill change is designed to remove. Top action when resuming: execute "Do now" item 4 in `orchestration-design.md` — update the act skill's three named sections (canonical-loop step 7, worktree `--push` trap reframing, auto-mode caveat reduced cost) so the new framing lands. `act-2204` (publish + release tag) and `act-b90e` (version-control the skill) remain the sharing-gate items from earlier sessions.

## What happened this session

Three interleaved threads:

**1. Processing the sift/ session output.** An earlier session's prompt was given to sift/ and produced a clean alpha-trial: `act init`, `.claude/settings.json` with the skill-recommended carveout, and 10 seeded issues. PR #14 is open in sift's repo, CI green, awaiting review. The session correctly stopped per instructions rather than running the loop. The meta-finding from that session was load-bearing: the settings allowlist did not actually opt the agent out of auto-mode's classifier — the classifier rejected the push with "bypasses PR review" reasoning regardless of the permission grant. Two independent gates, not one. The skill's claim that the settings entry is sufficient is out of date.

**2. Design discussion (Andrew + me, in conversation).** The implication of #1 is that the canonical act loop's "push after every close" step is at war with auto-mode in fresh projects. Two paths: drop the push-after-close property (loses multi-writer visibility) or stop having workers push to main and route integration through a separate orchestrator. We landed on the second: workers push to assigned branches; orchestrators handle main on their own cadence. The orchestrator is a *role*, not a specific tool — implementations include Claude Code with sub-agents (the parent session is the orchestrator, available today with documented workarounds), an external harness like gas town on beads, or a future built-in act-orchestrator. Mode A's "worker pushes main" becomes the degenerate case where worker and orchestrator are the same process. As a side benefit, an orchestrator that owns assignment can centralize dedup and batch claim/close ops, which directly reduces log noise — that's not act policy, it's orchestrator policy. Andrew sharpened one piece: act is and will remain git-coupled (the op log lives in git, full stop); the right question for non-developer-tools environments isn't "act on non-VCS substrate" but "where does act's git repo come from when the project being tracked doesn't have one."

**3. Documentation + review.** Wrote three new sections into `docs/orchestration-design.md` capturing the above. Filed `act-334e` to track external review. Dispatched three parallel subagent reviewers (abstraction soundness, implementation feasibility, test-case stress) all anchored at commit `94cbdf2` per skill discipline (pinned commit hash, "I read commit X" first-line requirement, >70% confidence floor, "what's working well" closing). Reviewers returned substantive findings; synthesized into:
- **7 inline edits** (commit `2d16efe`): softened three overclaims ("available today," "dissolves the classifier problem," "log noise is orchestrator policy not act policy"); expanded "Do now" item 4 to enumerate the three specific skill sections needing same-pass revision; split the multi-op-per-write open question into atomicity vs batching (two related but independent affordances); refined the Cowork open question per Andrew's git-coupled clarification; fixed a double-#4 numbering typo.
- **3 derivative issues**: `act-d264` (orchestrator branch-discovery surface — no act API today for "what branch is issue X being worked on"), `act-b5f8` (stale-claim recovery in Mode B — Mode A's "human notices" collapses to "orchestrator must algorithmically decide" but no policy is specified), `act-208e` (`bundle_strategy=per_session` is worker-scoped today; Mode B needs an orchestrator-scoped equivalent).
- Closed `act-334e` referencing the reviewers and derivatives.

## Real-time validation of the design

The auto-mode classifier blocked `act close --push` mid-session despite earlier pushes in the same session succeeding — the classifier got more conservative once the conversation context contained discussion of the issue. Andrew pushed manually as workaround. This is exactly the failure mode the push-asymmetry design dissolves: if the worker (this session) hadn't been the one trying to push to main, the classifier wouldn't have flagged it. Worth referencing in the skill update prose when "Do now" item 4 lands.

## Process note

The reviewer agent claimed "gas town wouldn't [know the branch]" as a specific assertion about a not-yet-built system. I relayed it without verification despite the auto-memory `feedback_verify_specific_factual_claims.md` covering exactly this pattern. Andrew caught it. Lesson: the memory's reach should extend to relayed reviewer findings, not just first-person claims. The same discipline applies when a subagent makes a specific named-system claim; the orchestrator (this agent) should verify or hedge before passing it on.

## Key artifacts produced

All under `/Users/andrewcove/Workspace/act/`:
- `docs/orchestration-design.md` — three new sections (Orchestrator implementations, push-asymmetry note in worker kernel, Log noise as control surface) + Do-now item 4 + open-question refinements. Two commits: `94cbdf2` (initial additions) and `2d16efe` (review-driven refinements).
- `.act/ops/act-334e/` — review task, claimed and closed in this session.
- `.act/ops/act-d264/`, `.act/ops/act-b5f8/`, `.act/ops/act-208e/` — three derivative issues filed this session.

## Backlog state

Top of queue includes predecessor items + this session's 3 additions. Run `act ready` for current actual ordering.

**Still open from predecessor sessions (unchanged):**
- `act-2204` (p=1) — flip aac/act public + cut fresh release tag. Sharing gate. Andrew's call.
- `act-ff5c` (p=1) — doc-drift prevention process. Brainstorm-first.
- `act-b90e` (p=2, probably should be p=1) — version-control the act skill. The "Do now" item 4 in orchestration-design (next session's planned work) edits the skill substantially; this would let the change be tracked.
- `act-8416` / `act-4fe6` (p=1) — Cowork / CC Web integration. Need external context.
- `act-5d6a` (p=1) — worktree subagent `--push` targeting bug. Still load-bearing for Mode B; the new framing in orchestration-design explains why it's a *contract clarification* rather than a workaround.
- `act-3c89`, `act-7ecd`, `act-4b45`, `act-f800`, `act-f2ea`, `act-dfa5`, `act-e6a5`, `act-8d67` (p=2/3) — small CLI improvements + investigations.

**From this session:**
- `act-d264` (p=2) — orchestrator branch-discovery surface design call. Required before any non-CC orchestrator runs.
- `act-b5f8` (p=2) — stale-claim recovery semantics for Mode B. Required before any orchestrator handles a worker crash.
- `act-208e` (p=2) — orchestrator-scoped bundle_strategy. Required before the orchestrator-batched-claims pattern reduces noise in practice.

## What to look at first when resuming

1. **Execute "Do now" item 4 in `docs/orchestration-design.md`.** This is the immediate concrete work: update three specific sections of `~/.claude/skills/act/SKILL.md` to land the push-asymmetry framing — the canonical-loop step 7 (push-to-main becomes mode-specific), the worktree-subagent `--push` trap section (reframe from "bug to work around" to "correct behavior"), and the auto-mode caveat section (reduced cost under orchestrator-owned pushes). Skill change only, no act code change. Probably also revise the review-policy section per "Do now" item 1.
2. **`act-b90e` (version-control the skill) probably worth promoting to p=1 and doing in the same pass.** The Do-now-item-4 changes are non-trivial; tracking them is more valuable than the recent skill-update history suggests.
3. **`act-2204` (publish + release tag).** Still the sharing gate from earlier sessions. Andrew was deep in act work the last two sessions but didn't make the public/release call. Re-decide.
4. **Review and merge sift PR #14** when the skill update lands — sift is the alpha-trial project for the new framing.
5. **Non-code stress test of act still parked** until Plugin Library work is set up. Strongest unresolved test of generality per orchestration-design.

## Cross-references

- Predecessor handoff: in git history (`...docs/session-handoff.md` at HEAD~N).
- Orchestration design (this session's main artifact): `docs/orchestration-design.md`
- Dogfood debrief (predecessor): `docs/aac-website-dogfood-debrief.md`
- Global act skill (target of next session's work): `~/.claude/skills/act/SKILL.md`
- Andrew's process-learnings: `~/Workspace/knowledge/_guides/process-learnings.md`
- sift/ project: `~/Workspace/sift/` (PR #14 open, awaiting review)

## Operational notes

- All session work was inside the act repo: 1 doc commit + 1 review-refinement doc commit + 3 derivative `act create` auto-commits + 1 `act close` auto-commit. All five pushed by Andrew manually after the in-session `act close --push` was rejected by the Claude Code auto-mode classifier.
- The auto-mode classifier blocked the close-push despite earlier session pushes succeeding, citing transcript context (the discussion of the classifier blocking pushes had sensitized it). Resolved by Andrew running `git push origin main` directly.
- No new auto-memory entries written this session — the existing `feedback_verify_specific_factual_claims.md` already covers the lesson about relayed reviewer findings; that file's scope just needs to be applied more broadly, not rewritten.
