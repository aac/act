# Session handoff — 2026-05-11

This session was strategic design + processing the first alpha-trial dogfood result, not landing code in act. Predecessor handoff (2026-05-10 night) is in git history; its top-of-queue items are still open and rolled forward below.

> **Quick read:** First real alpha trial of act ran in `~/Workspace/aac-website` — overnight Ralph Loop drained 23 issues across 20 iterations with 0 halts. Resulting meta-debrief produced 7 new act backlog issues (1 correctness gap + 6 UX), 5 sections of global act skill updates, 4 new process-learnings entries, and a parked design note (`docs/orchestration-design.md`) on Mode A vs Mode B and the orchestrator integration question. Top action when resuming: `act-5d6a` (worktree-subagent `--push` correctness gap) is the highest-leverage technical item from this session's batch; `act-2204` (flip repo public + cut release tag) remains the sharing gate from the predecessor session.

## What happened this session

Two interleaved threads:

**1. Strategic design — act + orchestrator integration.** Wrote `docs/orchestration-design.md`. Maps act onto Anthropic's managed-agents abstractions (session/harness/sandbox), names the two operating modes (Mode A: standalone loop where act is its own harness; Mode B: external orchestrator drives act-as-session), identifies layered ownership (act owns work-unit + claim atomicity + op log; orchestrator owns lifecycle + environment + dispatch; project CLAUDE.md owns how-to-work conventions), proposes a halt-signal contract (`HALT:` note prefix + nonzero exit + issue stays `in_progress`), enumerates three test cases (aac-website code work, Cowork tasks, Plugin Library business processes), and sequences what to do now vs. defer. No code changes — design only.

**2. Dogfood processing.** A separate aac-website session ran the canonical loop overnight (Ralph Loop, 20 iterations, 23 closes, 0 halts). After `BACKLOG_DRAINED` it produced a meta-debrief (`docs/aac-website-dogfood-debrief.md`) covering where the skill creaked, review signal-to-noise, near-misses, CLI friction, and one wrong-in-skill thing. This session turned the debrief into concrete outputs:

- **7 new act backlog issues filed and pushed:**
  - `act-5d6a` (p1, bug) — `--branch <ref>` for `create/update/close` to decouple op-writing from current working tree HEAD. Real correctness gap: a worktree subagent's `act create --push` landed ops on `origin/main` because `--push` follows branch tracking config, not commit target. Caused real damage in aac-website's `act-8808` (three op commits jumped ahead of work commit, required cherry-pick recovery).
  - `act-3c89` (p2) — `act show --full` to disable truncation.
  - `act-7ecd` (p2) — `act close --reason` validates length upfront, not on rejection.
  - `act-4b45` (p2) — `act ready` columns include `assignee` and `claimed_at`.
  - `act-f800` (p2) — `act log` needs `--since`, `--by-issue`, `--type` filters for retrospectives.
  - `act-f2ea` (p2) — `act doctor` as part of close to verify commit-marker correlation.
  - `act-dfa5` (p2, bug) — investigate why `per_session` bundling didn't collapse the close+commit two-step in aac-website (the agent ran it 15+ times during the loop). Either aac-website isn't on `per_session` (config gap → onboarding update) or there's a bundling logic gap.

- **Global act skill updated** (`~/.claude/skills/act/SKILL.md`, 5 sections):
  - Auto-mode caveat extended to cover `git merge --ff-only origin/worktree-*:*` and `git checkout main:*`, not just `git push origin main:*`. The aac-website loop tripped the classifier on the merge step even though `git push` was already whitelisted.
  - Reviewer prompts upgraded from "should pin commit hash" to **MUST** include "I read commit X at paths Y" as the first line of output before any findings. A reviewer that can't actually read the diff produces confidence numbers calibrated against nothing — they are speculative findings dressed up as analysis. Real damage in aac-website's `act-a9d0`: reviewer couldn't read worktree blobs and confidently flagged concerns at 80%+ that the actual code already handled.
  - Backlog-check generalized: required before any `act create` (mid-flight discovery, external-list translation, retrospective finding, anything), not just external-list translation.
  - Worktree subagent `--push` trap warning added until `act-5d6a` lands, with three explicit workarounds.
  - `bundle_strategy=per_session` referenced as the answer for projects that want quieter git history.

- **Andrew's `~/Workspace/knowledge/_guides/process-learnings.md` updated** (4 new entries):
  - Assert at the user-visible boundary, not at a proxy for it.
  - Repo-level guardrails are institutional memory.
  - Don't claim verification of what you couldn't read.
  - Check the backlog before filing.

## Key artifacts produced (all under `docs/`)

- `orchestration-design.md` — design synthesis (this session)
- `aac-website-dogfood-debrief.md` — first alpha trial debrief (written by the aac-website session, lives here for cross-reference)

Both are added to this commit.

## Backlog state

Top of queue includes predecessor items + this session's 7 additions. Run `act ready` for current actual ordering.

**From predecessor (still open, unchanged):**
- `act-2204` (p=1, blocking) — flip aac/act public + cut fresh release tag. The canonical-pitch verification gate. Andrew's call.
- `act-ff5c` (p=1) — doc-drift prevention process. Brainstorm-first.
- `act-b90e` (p=2 but probably p=1) — version-control the act skill file. Important for sharing; skill updates landed this session, making this more urgent.
- `act-8416` (p=1) — Cowork integration. Needs external context.
- `act-4fe6` (p=1) — CC Web integration. Needs external context. Benefits from act-2204 landing first.
- `act-e6a5` (p=2) — brew tap / curl installer. Lower urgency now that go install is canonical.

**From this session:**
- `act-5d6a` (p=1, bug) — the highest-leverage technical item from this batch. Mode B prerequisite.
- `act-3c89`, `act-7ecd`, `act-4b45`, `act-f800`, `act-f2ea`, `act-dfa5` (p=2) — small CLI improvements + the per_session investigation.

## What to look at first when resuming

1. **`act-5d6a`** — highest-leverage technical work from this session's batch. Fixes the worktree-subagent op-targeting bug; required for Mode B to be robust. Could be the next dogfood loop's target if another act-on-act session runs.
2. **`act-2204` still the sharing gate.** Andrew was deep in act work today but didn't make the public/release decision. Conversation in earlier sessions suggested "maybe there isn't a reason for it to stay private" — re-decide and act.
3. **`act-dfa5` is a near-zero-cost check on the aac-website side.** Is the repo on `bundle_strategy=per_session` or not? Could be done by the aac-website session next time it's active. If yes → real bundling gap to fix in act. If no → config-onboarding gap to fix in the skill / `act init` default.
4. **Non-code stress test of act parked** until Andrew sets up Plugin Library work. The orchestration-design note flags this as the strongest unresolved test of generality; aac-website only validated code work.
5. **`act-b90e` worth promoting to p=1.** This session updated the global act skill significantly; the install-and-go promise depends on the skill being available to a friend's agent, and it currently isn't version-controlled.

## Cross-references

- Predecessor handoff: in git history (`5713cae docs: session handoff captures act-6051 + 3 follow-ups + act-2204 finding`).
- Orchestration design note: `docs/orchestration-design.md`
- Dogfood debrief: `docs/aac-website-dogfood-debrief.md`
- Global act skill (updated this session): `~/.claude/skills/act/SKILL.md`
- Andrew's process-learnings: `~/Workspace/knowledge/_guides/process-learnings.md`

## Operational notes

- All session work outside the act repo, plus 7 `act create` calls inside it (+ their auto-commits, all pushed). No code touched in `internal/` or `cmd/`.
- Auto-memory written from this session: `feedback_solo_repo_merge_policy.md` — for solo/personal repos with agent loops + in-session review, prefer direct ff-merge to main over GitHub PRs (PR review is theater in that context).
- The aac-website session may still be running; its handoff was refreshed mid-loop and is stale relative to the rest of its closes. It'll refresh on its own when next active; not this session's job to refresh.
