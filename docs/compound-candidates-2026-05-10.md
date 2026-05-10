# Compound candidates — 2026-05-10 dogfood/v0.2 session

Five process learnings surfaced over the course of this session that don't appear (or appear too vaguely) in `~/Workspace/knowledge/_guides/process-learnings.md`. Per the `/compound` workflow, these are *proposals* — Andrew approves, rewords, or skips before they land in the KB. Captured here in the act repo so they survive the morning.

I de-duped against the existing process-learnings.md. Each candidate below is either net-new or sharpens a related entry I cite.

---

## 1. Cold-start agents apply documentation literally — gaps are skipped, not interpolated

When a fresh agent reads a runbook (CLAUDE.md, README, slash-command body) without conversation history, it executes exactly what's written. "Obvious next steps" left implicit don't get inferred — they get omitted.

*Source:* The first dogfood sub-agent on `act-6bbd` followed CLAUDE.md's 7-step loop perfectly, then stopped without pushing because the loop ended at "repeat from step 2." `git push` was missing entirely from the doc; the agent didn't infer it from context. Caused 3 commits to live local-only on its branch.

*Why distinct from existing "Write prompts that produce self-sufficient agents":* That entry is about supplying enough *context*. This is about agent *reading mode* — they don't paraphrase, smooth, or fill gaps. Implication: any operational doc for cold-start agents needs to be operationally complete, not gestural.

---

## 2. Multiple review modalities catch non-overlapping defect classes

A static code review, an exploratory UX walkthrough, and actual workflow runs each find bugs the others miss. Relying on any single modality leaves a systematic blind spot.

*Source:* In one session for `act`:
- The UX-eval agent (synthetic walkthrough in a sandbox) found inconsistent commit-message formats and "prefix ok" docs that lied — both invisible to the code reviewer.
- The code reviewer (static analysis) found `act ready` returning `in_progress` issues and HLC tiebreak diverging from claim tiebreak — both invisible to the walkthrough.
- The actual workflow runs (sub-agents shipping issues) found the claim+rebase failure on no-upstream and the git-index serialization gotcha — invisible to both other modalities until exercised.

*Why distinct from existing "Review agent output before deploying":* That entry is about *gating*. This is about *coverage*. The implication is different: don't pick one review type; budget for ≥2 with different shapes.

---

## 3. Track meta-work in the same tracker as feature-work

Reviews, audits, retrospectives, dogfood evaluations — give them tickets with the same lifecycle as features (claim, work, derivative follow-ups, close). The audit trail is the value, not operational overhead.

*Source:* Filed the codebase review itself as `act-da03` rather than spawning ad-hoc. When the reviewer returned, the close message listed 9 derivative issues filed from its findings. Result: any future reader can answer "what reviews have we done, what did each find, what came of them" from the tracker alone — without crawling commit history or chat transcripts.

*Why this matters:* Without tickets, meta-work is invisible to the same tools that surface feature-work backlog. When meta-work matters at all, it deserves equal visibility — and conversely, if you can't be bothered to file a ticket for it, that's a signal the work probably doesn't need to happen.

---

## 4. The coordinator role is more valuable than the worker role at scale; preserve coordinator context aggressively

When orchestrating multiple sub-agents, spending your own conversation context on production work is a false economy — that context is needed for integration decisions, conflict resolution, prioritization shifts, and synthesizing parallel reports. Production work is delegable; coordination is not.

*Source:* Mid-session I caught myself oscillating between "spawn an agent" and "do it myself." The latter felt productive (visible progress, no token spend) but burned context that would have been more valuable later when 3 agent reports landed and needed synthesis. The right cut: delegate clearly-scoped work to worktree-isolated agents, keep my context for integration and decisions.

*Why distinct:* Existing entries cover *when* to delegate and *how* to write good prompts. Missing piece: the coordinator's context is itself a finite resource that should be conserved like any other.

---

## 5. Autonomy modes are "autonomous on already-made decisions; halt on still-open ones"

When a flag like Claude Code's auto-mode says "execute autonomously," the right reading isn't "make every decision yourself." It's "execute the decisions you've already made; halt and surface the ones still open." Judgment calls that haven't been settled aren't candidates for autonomous resolution.

*Source:* Andrew enabled auto-mode early but still made specific judgment calls along the way: push cadence ("after every close"), worktree isolation, whether to do reviews, whether to publish the draft release. Auto-mode worked when the decision shape was settled and only the execution remained. When I (auto-mode-enabled) tried to make new architectural calls without checking, friction surfaced.

*Why distinct:* Existing "self-sufficient agents" is about giving agents enough context to act. This is about the *boundary of autonomy* itself — don't auto-execute decisions still in flux.

---

## Pruned candidates (mentioned for completeness; not recommending)

- "File-level concurrency primitives don't save you from process-level resource contention" — already covered well enough by existing "Isolate parallel work that touches shared files."
- "Confidence-filtered reviewers beat exhaustive reviewers" — true but specific to review-prompt design, fits better as a hint in a review skill than a process principle.
- "Plain-language summaries vs tool firehose" — communication style preference, repo-specific, not generalizable enough.

## Recommended action

Read the five candidates above. Strike, reword, or combine as you see fit. When you're satisfied with a final list, the standard `/compound` step is: append to `~/Workspace/knowledge/_guides/process-learnings.md` under the appropriate sections, bump `last_updated`, and commit to the knowledge repo. Several of these likely fit under "Delegation and orchestration"; #1 might fit under "Prompt and system design"; #3 under "Iterating" or as a new section.

I left this file in `act/docs/` rather than the KB so the KB remains your-curated. If we end up keeping all five, this scratchpad file can be deleted in the same KB-commit session.
