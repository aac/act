# act — evaluation

A project-level read on whether act is worth using for agent-driven work, drawing on the dogfood loop, the 2026-05-10 code review, and the four sub-agent runs that exercised it cold.

## Vs. Claude's default markdown approach

Claude's defaults for tracking work are `TodoWrite` (in-conversation list) and ad-hoc markdown files. They have one virtue: zero infrastructure. Everything else is a cost.

- **Ephemeral.** TodoWrite state lives in the conversation; once it ends, the list is gone. A markdown checklist in the repo survives but isn't structured — no claim, no dep semantics, no "what's ready," no machine-queryable interface.
- **No multi-agent coordination.** Two parallel agents working from the same `TODO.md` will trample each other or duplicate work. There's no atomic claim.
- **No audit trail.** Who closed what, when, why? `git log -- TODO.md` is the closest thing, and it's noisy because every edit is one commit.
- **No agent-native primitives.** `act_next`, `act_finish`, `act_block` compose what would otherwise be 4-5 distinct steps. With markdown, agents reinvent the workflow per session.

Where defaults win: throwaway sessions, one-off scripts, anything where the "is this still relevant?" question can be answered by reading 50 lines. The crossover where act starts paying for itself is around **multi-session work or 2+ concurrent agents**. Below that threshold, act is overhead.

## Vs. beads

We didn't deeply re-research beads in this session, but the architectural divergence captured in `docs/brief-v4.md` still holds: beads runs on Dolt (a git-versioned MySQL-compatible database). act uses an append-only op-log in plain git. Tradeoffs:

- **Dependencies.** beads requires Dolt (a meaningful runtime dep). act is a single Go binary; the only dep is `git`. For a "drop into any repo" tool, the latter is a much lower bar.
- **Surface area.** beads has accreted features (the brief calls this out as a motivating concern); act is deliberately scoped to ~12 commands.
- **Commit pattern.** Dolt commits database transitions as units, so a single beads transaction = one Dolt commit. Historically this was act's biggest structural disadvantage. **As of act-728d (per_session bundling) + act-a659 (close stages into work commit), a typical issue lifecycle on `bundle_strategy=per_session` produces 2 commits (claim + work-with-close) instead of the original 4-5.** The Dolt-style "transaction = one commit" property is now approximated in plain git, without the Dolt dependency. The structural gap has narrowed substantially.
- **Scale.** beads is built for teams; act is scoped to "solo to small multi-agent." act will hit the git index serialization wall (we saw this) before beads hits whatever Dolt's contention story is.

For a personal-to-small-team agent-driven workflow, act's simpler surface and simpler dependencies are real value. For an org-scale agent rollout, the beads tradeoffs may pay off — Dolt query performance, multi-tenancy, etc. We won't know until we run a serious alpha trial.

## Agent intuition

Strong evidence from the dogfood: **four cold-start sub-agents** picked act up from CLAUDE.md + `act help` and shipped real fixes end-to-end. None halted on confusion about the workflow itself; the friction they reported was always about the *implementation* (where does X live, what's the error envelope shape), not the *concept* (how do I claim, work, close, push).

The canonical loop (`ready → claim → work → commit-with-marker → close → push`) is small enough to hold in head and concrete enough to execute literally. The MCP composed tools (`act_next`, `act_finish`) compress that to two calls. Both surfaces feel right.

Caveats:

- Documentation drift bit hard: the prefix-matching docs lied about what worked, and the canonical loop in CLAUDE.md was missing `git push` for one full agent run. Both fixed, but: agents trust docs literally. The cost of doc drift is high.
- The three p=0 showstoppers we just closed (claim+rebase failure, ambiguous-id exit code, prefix matching) would each have been blocking for a fresh repo. The bar for "intuitive" is zero of those.

Net: yes, intuitive enough — *now*. Probably wasn't a week ago.

## Architecture and scalability

The reviewer's report (2026-05-10) is the cleanest single source. Highlights:

> *HLC ("Hybrid Logical Clock") is how act orders events across distributed writers. Each op has a stamp `(wall_ms, logical_counter, node_id)` that combines physical wall-clock time with a Lamport-style logical counter. Two agents on different machines writing conflicting ops resolve deterministically: compare wall time first, then logical counter, then op-hash for tiebreak. It's what makes "no central server, just git pull --rebase" produce a coherent merged state.*

**Strong:** HLC implementation matches the spec algorithm exactly; op-envelope canonicalization gives genuine determinism (property tests + fuzzer back this up); the claim protocol is correct end-to-end including the no-upstream and idempotent-self-claim cases; error-envelope shape is uniformly enforced; the `WriteOpAndAutoCommit` rollback prevents partial-write corruption.

**Real concerns (still open):**

- HLC tiebreak by NodeID vs claim tiebreak by op_hash — divergent rules in different code paths (act-492e). Latent multi-writer correctness gap.
- LWW-over-terminal-state hole: a claim with later HLC than a close could in principle resurrect a closed issue (act-b7ad). Edge case, real bug.
- ~~`IsValidID` caps at 16 hex; spec implies 40 internally (act-7574). Latent until prefix collisions push past 16.~~ Resolved: spec clarified to authoritatively state on-disk ids are short-form, 4..16 hex; the 64-hex sha256 digest is an internal derivation value, never written as an `id`/`issue_id` (act-7574).

**Closed since first-pass eval:** act-6181 (act create JSON shape) shipped. The four-blocker list at first-pass alpha-readiness has narrowed to three latent multi-writer issues — none of which would bite a solo or small-multi-agent trial.

**Scalability ceiling:** the architecture is solid up to single-digit concurrent writers per repo. Beyond that, the git index becomes the bottleneck (commit-per-issue × N writers = serialization, even with bundling). For target use ("solo to small multi-agent"), this is fine. If act ever needed to scale to 10+ concurrent writers, the design needs rethinking.

## Commit log noise

This *was* the single concern most likely to bite a real production user. As of 2026-05-10 it's substantially mitigated — see "What shipped" below — but still worth understanding the original shape because it informs `bundle_strategy=per_op` (the legacy default, retained for dogfood/dev use).

In the original `per_op` model, closing one issue produced 4-5 commits in the act-only category (claim + work + close + maybe deps), on top of the actual work commits. In `act` itself the repo was dominated by `act-op:` commits.

**What shipped (act-6018 → act-728d → act-a659):**

1. **act-728d** introduced `bundle_strategy` config with two values: `per_op` (legacy, every op auto-commits standalone) and `per_session` (claims and intermediate ops auto-commit; close ops *bundle pending act-op commits* into the close commit so they reach the remote as one unit).
2. **act-a659** went further: under `per_session`, when the working tree has uncommitted non-`.act/` changes, `act close` *stages* the close op rather than committing it. The agent's next `git commit -am '<msg> (act-XXXX)'` subsumes the staged close. Net effect on a typical loop: 2 commits (claim + work-with-close), not 3-5.

The original three-options analysis was:

1. **Bundling with periodic flush** — agents auto-commit off, `act flush` groups pending ops.
2. **Separate-branch ops** — `.act/` ops on `refs/act/ops`; main stays clean.
3. **Squash-on-close** — close commit incorporates prior op commits as a sequence.

What landed is closest to (3), via a config-flag-controlled stage-and-fold mechanism that requires no separate `flush` command and keeps `.act/` in the working tree (preserving the "ops are visible to git diff" property). The Dolt "transaction = one commit" pattern is now approximated in plain git on per_session repos. For repos where readable git history matters (i.e. nearly all of them), `per_session` should be the default; `per_op` remains useful for active act development where seeing every op individually is the whole point.

**Open uncertainty:** whether `per_op` should be deprecated outright once another repo has run on `per_session+act-a659` long enough to confirm there's no signal lost. Captured in CLAUDE.md's "known stale areas" list.

## Skill activation validated (2026-05-10)

After the global skill at `~/.claude/skills/act/SKILL.md` shipped, a fresh sub-agent in a brand-new `/tmp/skill-test` repo (just `.act/` + the binary, no `CLAUDE.md`) was given a deliberately generic prompt — "look around, figure out how this project tracks work, complete the next thing." It self-bootstrapped: spotted `.act/`, triggered the skill, ran `act help`, walked the canonical loop end-to-end, committed with the marker, closed cleanly.

The "drop into any repo" promise from the skill design holds up empirically. One harness wrinkle (Claude Code's auto-mode policy blocks `git push origin main` even when the loop authorizes it) is filed as `act-e5b8` — a small skill addendum advising the `.claude/settings.json` allow-rule.

## Honest readiness read

**Yes for alpha trial in a small personal repo.** The named adoption blockers are gone; the workflow loop survives cold-start agents; the architecture is correct in the cases that matter for solo-to-small-multi-agent use; commit-log noise — the previous biggest concern — is resolved on `per_session` repos.

**Closer to "drop into any team repo," but not yet.** The remaining gap is the missing distribution path: no brew tap, no curl-installer, draft release. Tracked across act-6051 (canonical bootstrap decision), act-e6a5 (brew tap), act-4fe6 (Claude Code Web), act-8416 (Cowork). Once one of those graduates from "filed" to "lands," the adoption story is essentially complete for the target audience.

**The bigger question (resolved):** the original eval asked whether the per-op-commit model was load-bearing for the architectural thesis or an implementation choice hideable behind a flag. **Answer: hideable.** act-728d shipped `bundle_strategy`; act-a659 shipped close-stages-into-work-commit. The audit trail (every op present as a file under `.act/ops/`) and the multi-writer concurrency story (HLC stamps, no central server) are both preserved at any bundling granularity, because they live in the op files, not in the git commit boundaries. The structural concern that drove this whole investigation has been retired.

**What changes the read next:** a real alpha trial in another repo. Until that happens, all of the above is informed-but-internal — we're confident in act based on dogfood evidence and cold-start sub-agent runs, but the proof is "does someone *else's* repo benefit from this," and that experiment hasn't been run yet.
