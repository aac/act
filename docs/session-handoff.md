# Session handoff — 2026-05-13 (evening)

Short session, late-day: looked at downstream commit-log noise data from inbox-triage, filed a new act core issue + a cleanup chore in inbox-triage, and sketched a four-phase plan for moving act forward. **Top priority for the next session has not changed from the predecessor handoff** — it is still agent-push-to-main. The work below is supporting context that arrived in flight; nothing in it should redirect tomorrow's session away from Phase 1.

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

## Four-phase plan beyond Phase 1

Established in conversation this session; not yet filed as umbrella issues. If Phase 1 lands tomorrow and there's still session time, Phase 2 is the natural next move. Otherwise these queue for following sessions.

- **Phase 2 — noise-reduction synthesis.** Consolidate `act-6c73` + `act-208e` + `act-dfa5` into one design pass. They overlap enough that doing them serially produces inconsistent recommendations. Output: updated `docs/commit-noise-design.md` (or new note) and one implementation issue.
- **Phase 3 — CLI polish + data-model bugs.** Parallelizable across sub-agents because the items are disjoint — though per CLAUDE.md, watch for `cmd/act/internal/cli` merge conflicts. Items: act-4b45, act-7ecd, act-3c89, act-b891, act-982a, act-56a0, act-f800 (CLI polish); act-8c78, act-b7ad, act-492e, act-7574 (deeper bugs).
- **Phase 4 — sharing/adoption (gated on Phase 1 working).** act-2204 (publish), act-e6a5 (brew/curl), act-8416 (Cowork), act-4fe6 (CC Web). Should not happen until the loop actually runs autonomously, or we'd onboard others into a known-broken thing.

The longer design tasks (`act-d264` branch-discovery, `act-b5f8` stale-claim recovery, `act-8d67` separate-repo model) slot in around Phase 2/3 as Andrew-availability permits.

## What happened this session

Three things, in order:

1. **Reviewed inbox-triage commit-log noise data.** Another Claude session evaluated inbox-triage's git log at Andrew's request: 69 of 99 commits (~70%) are pure act-op metadata. Dominant ops are NOT close — they're create (32), close (10), tombstone (6), add_dep (6), add_accept (6, with 5 consecutive on a single ticket), claim (5), update_field/reopen (3). Diagnosis: act's existing close-time bundling assumes claim → work → close, but the actual noise is from metadata-only ops that have no work commit to fold into — pre-claim grooming runs (create + add_dep + add_accept) and same-field rewrite bursts.

2. **Filed `act-6c73`** (p2 bug) in this repo: "handle pure-metadata act-op runs that close-time bundling can't fold (inbox-triage downstream data)." Includes the full inbox-triage stats, the two structural patterns close-time bundling can't help with (pre-claim runs + same-field bursts), and proposed-scope-for-investigation (amend-on-write vs explicit-batch-mode vs do-nothing). Linked as `relates` to act-208e, act-dfa5, act-6018.

3. **Filed `act-5704`** in `~/Workspace/inbox-triage`'s own backlog: "reduce commit-log noise from act-op runs (local cleanup pending core fix in act-6c73)." Two parts: one-time history rewrite (inbox-triage has no git remote, fully safe to squash consecutive metadata-op runs); going-forward wrapper script under `.claude/` that amends instead of new-commit when HEAD is a same-session metadata-only act-op. Closes when act-6c73 lands proper core support. This means the noise problem is tracked where it can actually be fixed today, without waiting for act core changes.

## Why we did NOT kick off Phase 1 work tonight

Andrew was at end-of-day. Phase 1 needs investigation + verification + a real loop run; not fire-and-forget. The history-rewrite + wrapper in inbox-triage (act-5704) is the one near-term cleanup we identified, but it touches inbox-triage's history and the wrapper design has edge cases (pushed-status detection, session identity, concurrent worktrees) — also wanting Andrew awake. Both queued.

## Key artifacts produced

- `~/Workspace/act/.act/ops/act-6c73/` — new bug, links to act-208e/dfa5/6018, includes inbox-triage stats.
- `~/Workspace/inbox-triage/.act/ops/act-5704/` — new chore in inbox-triage's backlog covering both the rewrite and the wrapper.
- This handoff (the previous afternoon handoff is in git history; its content is rolled forward into the TOP PRIORITY block above).

## Backlog state

Run `act ready` in this repo for current ordering. Nothing this session changed Phase 1 priority. The afternoon handoff's "still open from predecessor sessions" block remains accurate. New this evening: `act-6c73` (p2, this repo).

## What to look at first when resuming

1. **Phase 1: agent-push-to-main.** Top priority. Re-read the TOP PRIORITY block above and the predecessor 2026-05-13 (afternoon) handoff in git history if more context is needed.
2. **Sub-task: promote `act-b90e` to p1** and pair it with Phase 1 so the skill changes from "Do now item 4" are tracked.
3. If Phase 1 is fully landed: Phase 2 (noise-reduction synthesis — `act-6c73` + `act-208e` + `act-dfa5` consolidated design pass).
4. `act-2204` (publish + release tag) is still the sharing gate. Re-decide once Phase 1 validates.
5. **Review and merge sift PR #14** when the skill update lands — sift is the alpha-trial project for the new framing.

## Cross-references

- Afternoon predecessor handoff: in git history (`docs/session-handoff.md` at HEAD~N).
- Orchestration design: `docs/orchestration-design.md` (Do-now item 4 is the structural fix for Phase 1).
- Commit-noise design note: `docs/commit-noise-design.md` (predates today's inbox-triage data; Phase 2 should refresh it).
- Global act skill (target of Phase 1 changes): `~/.claude/skills/act/SKILL.md`
- inbox-triage's own act backlog: `~/Workspace/inbox-triage/.act/`
- sift/: `~/Workspace/sift/` (PR #14 open, awaiting review)

## Operational notes

- Work this session: 1 `act create` in this repo (act-6c73), 3 `act dep add` ops linking it, 1 `act create` in inbox-triage (act-5704). All auto-committed; this repo's commits pushed to origin/main; inbox-triage has no remote.
- No `act close` ops this session — none of the work this session was a tracked issue.
