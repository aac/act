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
- **Commit pattern.** Dolt commits database transitions as units, so a single beads transaction = one Dolt commit. act commits per op, which produces a noisier git log (see below). This is the biggest structural difference and the one most worth investigating before recommending act for repos where git log readability matters.
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

**Real concerns:**

- HLC tiebreak by NodeID vs claim tiebreak by op_hash — divergent rules in different code paths (act-492e). Latent multi-writer correctness gap.
- LWW-over-terminal-state hole: a claim with later HLC than a close could in principle resurrect a closed issue (act-b7ad). Edge case, real bug.
- `IsValidID` caps at 16 hex; spec implies 40 internally (act-7574). Latent until prefix collisions push past 16.
- `act create` JSON shape doesn't match the spec — agents reading the spec will fail on first parse (act-6181).

None of these are showstoppers for an alpha trial. All are filed.

**Scalability ceiling:** the architecture is solid up to single-digit concurrent writers per repo. Beyond that, the git index becomes the bottleneck (one commit-per-op × N writers = serialization). For target use ("solo to small multi-agent"), this is fine. If act ever needed to scale to 10+ concurrent writers, the design needs rethinking — likely via the bundling investigation (act-6018).

## Commit log noise

This is the single concern most likely to bite a real production user. Closing one issue produces 4-5 commits in the act-only category (claim + work + close + maybe deps), on top of the actual work commits. In `act` itself the repo is dominated by `act-op:` commits.

Mitigation paths (act-6018 tracks this):

1. **Bundling with periodic flush** — agents auto-commit off, `act flush` groups pending ops. Probably the right default for production.
2. **Separate-branch ops** — `.act/` ops on `refs/act/ops`; main stays clean. Loses the "ops in working tree" property.
3. **Squash-on-close** — close commit incorporates prior op commits as a sequence. Loses individual op timestamps from git log (still preserved in op-file HLC).

Recommendation: implement (1) behind a flag; default current behavior in dogfood/dev mode, default bundling in production mode. Beads-via-Dolt sidesteps this entirely because Dolt commits transactions, not per-row changes — that's the structural difference most worth borrowing the idea of.

## Honest readiness read

**Yes for alpha trial in a small personal repo.** The named adoption blockers are gone; the workflow loop survives cold-start agents; the architecture is correct in the cases that matter for solo-to-small-multi-agent use.

**Not yet for "drop into any team repo."** Two reasons: commit-log noise (real friction in any repo where humans read the log) and the missing distribution path (no brew, no curl, draft release). Both filed.

**The bigger question act-6018 will answer:** is the per-op-commit model load-bearing for the architectural thesis (multi-writer concurrency, audit trail) or an implementation choice that can be hidden behind a bundling flag without losing those properties? If the latter, act is much closer to broadly useful than it looks today.
