# Session handoff — 2026-05-09 → 2026-05-10

Andrew's session bootstrapping `act` to dogfood itself, then grinding the v0.2 backlog while reviewing the resulting DX. Ended with Andrew going to bed and asking the loop to keep cranking overnight.

## Where things stand

**Closed this session (13):**
- act-a854 — `act help` is now a real agent-onboarding tutorial (also `act help workflow` / `act help ops-model` / `act help errors`)
- act-0735 — CLAUDE.md (the agent-runtime rules layer for this repo)
- act-ac52 — push step added to CLAUDE.md's canonical loop (the first dogfood agent had committed-and-stopped)
- act-6e2b — CLAUDE.md now requires `isolation: "worktree"` for sub-agents (un-isolated agents collide on git index)
- act-aa8c — `act help workflow` documents commit_marker invariants (format, source, doctor's substring guarantee)
- act-acd9 — `act help errors` documents the error-envelope contract (shape, code list, byte-counted lengths)
- act-77e6 — `internal/hooks` test now resolves `true` via `$PATH` so it runs on macOS, not just Linux
- act-c1be — `act foo` and `act dep --help` no longer share the misleading "not implemented yet" message
- act-6bbd — `--description-file <path>` (and `-` for stdin) on create/update
- act-5467 — `act_next` returns a `commit_marker` field; `act show <id> --commit-marker` for non-MCP callers
- act-63a1 — `act dep add --blocks` / `--blocked-by` directional flag aliases
- act-fdb2 — `act update --claim` no longer breaks on local-only repos and is idempotent on re-claim by same node

**In flight when the session paused:**
- `act-da03` — overall code review of the codebase (claimed by parent session; reviewer agent in flight at the time of writing). Findings get filed as derivative act issues; this issue closes with a pointer list.
- `act-d3a5` — auto-commit message inconsistency. Worktree branch `worktree-agent-a5db7f3785fa19dd3`. Will need merge + push when it returns.

**Ready (highest priority first):**
- act-6fca (p=0) — id prefix matching contradicts every doc; the docs say "prefix ok" but only the full short id works. Showstopper for any new repo because agents will trust the docs.
- act-c26a (p=1) — `act create --blocked-by` + composed `act_file_blocker` MCP tool with atomic rollback.
- act-10f7 (p=1) — `act show` text mode hides description and commit_marker; reach-for-jq friction.
- act-982a (p=2) — `act dep add` and `act show` display strings read inverse to actual dep semantics.
- act-6218 (p=2) — `act create` misparses titles starting with `--`.
- act-b891 (p=2) — `act show --include-ops` is a no-op without `--json`.
- act-56a0 (p=2) — `act log --summary` one-line-per-op timeline view.
- act-c93b (p=2) — `act mine` and `act ready --mine`.
- act-f2c7 (p=3) — UX polish nits (5 small things bundled).

## What to look at first when you resume

1. **Reviewer findings** (`act show act-da03`). The first overall code review's report — likely 5-15 derivative issues already filed. Skim severities.
2. **act-d3a5 status.** If the worktree agent finished overnight, branch is at `worktree-agent-a5db7f3785fa19dd3`; merge + push it. If still running, leave it.
3. **Noise-reduction options** (your earlier ask). claude-code-guide came back with concrete answers; key ones:
   - Session-level: `/focus` — collapses tool calls into one-line summaries; toggle on/off.
   - Project-level: add `{"tui":"fullscreen","viewMode":"focus"}` to `.claude/settings.json` in this repo so future sessions in `act/` start collapsed.
   - These weren't applied — they're your call.
4. **Loop-includes-review update.** CLAUDE.md should grow a step: "before close, request review of the diff." Not done; waiting for the first review to land so we know what review-as-step actually looks like in practice.

## DX/UX observations from this session

What I'd lift into a future skill or guide:

**Working well:**
- `act help` as the canonical onboarding doc; sub-agents read it and start being useful with no other prompt.
- CLAUDE.md as a per-repo rules layer on top of `act help` (mechanics) — easy to iterate as we discover what's load-bearing.
- Worktree-isolated agents in parallel; the multi-writer thesis from the brief actually plays out cleanly once each agent has its own working tree.
- `act ready` + dep gating (we used it to make all v0.2 ergonomic work wait on the two p=0 docs issues).
- File-bug-and-keep-going pattern — sub-agents find issues mid-flight, file them, finish their own work without halting.
- `act reopen` after a wrong close — UX-eval explicitly called this out as the recovery story working as designed.
- The op-log audit story (`act log <id>`); UX-eval said don't get more clever here.

**Real bugs the dogfood loop surfaced (not the eval, the actual loop):**
- Un-isolated parallel sub-agents collide on git index even with disjoint files.
- Original CLAUDE.md loop didn't include `git push`; first sub-agent committed-and-stopped silently.
- `act update --claim` left the claim op written but failed exit code on local-only repos.
- Idempotent re-claim by the same node returned "Lost claim race" against itself.
- Doctor uses `id[:8]` not `ShortestUniquePrefixes`; the relationship is invariant-by-coincidence and not documented.
- Commit messages have three different shapes including buggy `act-act-` and empty `act- create` forms (in flight via act-d3a5).
- Display direction of dep edges reads inverse to actual semantics.
- ID prefix lookup is documented as supported but only full-short-id works.

**Patterns I'd codify:**
- Sub-agents in worktrees should be told "push to your branch, not main" explicitly — CLAUDE.md's step 7 misled the first worktree agent.
- "Claim → work → commit-with-marker → close → push" is the right unit; making any of those steps optional weakened the loop.
- Reviews need to be tracked in act, not just spawned ad-hoc. Same audit trail as feature work.
- "File mid-flight discoveries, don't halt" produced 7+ real bugs in a single session without derailing any individual issue.

## Viability for other projects

**Not yet, but close.** Two showstoppers block adoption right now:
- act-6fca (prefix matching) — agents in any new repo will trust the "prefix ok" docs and immediately get burned.
- act-d3a5 (commit message bugs) — `act-act-` and empty-id forms would pollute the user's git history; doctor's grep would miss some commits.

Both will be done shortly (d3a5 worktree in flight; 6fca is next-up after the review). After they land, plus whatever the review surfaces, an alpha trial in a small read-mostly project (a knowledge repo, a personal tool repo) is reasonable — agents would use act, we'd watch the trace, fix what breaks.

**Full "drop into any repo" readiness** wants: a global Claude Code skill that triggers on the presence of `.act/` and contains the patterns above; a `brew install` tap or `curl … | sh` so installation isn't `go build`; a published GitHub Release (currently draft). That's another arc of work, not today.

## Operational notes

- Three worktrees from earlier merged agents were cleaned up at session end (branches deleted local + remote). Only one worktree currently live: the d3a5 agent's.
- Two background agents finished and reported back during the session (UX-eval, claude-code-guide); their full reports are in the conversation transcript. The actionable bits are filed as act issues or reflected here.
- `bin/act` is gitignored; rebuild with `go build -o bin/act ./cmd/act` if missing.
