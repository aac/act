# Session handoff — 2026-05-09 → 2026-05-10

Andrew's session bootstrapping `act` to dogfood itself, then grinding the v0.2 backlog while reviewing the resulting DX. Ended with Andrew going to bed and the loop cranking through three more worktree agents before stopping.

> **Quick read:** All three p=0 showstoppers closed (claim+rebase, id_ambiguous exit code, id prefix matching). 17 issues closed total. 17 still ready, top of queue is now p=1. **act is meaningfully closer to being usable in another project — the explicit adoption blockers are gone.** Remaining work is polish + the marquee composed primitive (act-c26a). Read `docs/compound-candidates-2026-05-10.md` for five process learnings I'd propose to your KB.

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

**Closed after the original handoff (overnight stretch):**
- act-da03 — overall code review (closed with pointers to 9 derivative issues)
- act-d79b — act ready was returning in_progress and blocked issues; spec says only open
- act-d3a5 — commit messages now use canonical `act-op: (act-XXXX) <op>` everywhere; killed the act-act- and empty-id bugs
- act-8dcd — ambiguous-id-prefix now exits 2 across all commands per the spec's universal table
- act-6fca — true unique-prefix id resolution (any non-empty hex prefix that uniquely matches now works); MinInputHexLen=1 for lookup, MinShortHexLen=4 still governs display

**In flight at session end:** none. All three worktree agents (d3a5, 8dcd, 6fca) returned, branches merged, tests green, pushed.

**Conflicts surfaced and reconciled:** 6fca's branch was off pre-8dcd main; their TestRunShow_ShortPrefixAmbiguous asserted exit 3 (the old behavior) and a CLAUDE.md note said id_ambiguous=exit 3. Both reconciled to 8dcd's exit-2 in commit c4b22e0. No behavioral conflict.

**Ready (highest priority first, no p=0 left):**
- act-a319 (p=1) — CLAUDE.md should grow a review step in the canonical loop (your earlier ask). Lessons from this session's first review captured in the description.
- act-6181 (p=1) — `act create --json` shape diverges from spec (no `ok`, `prefix` vs `short_id`, missing `op_id`/`committed`/`pushed`).
- act-d9c7 (p=1) — default priority is 1 in code, spec says 2.
- act-10f7 (p=1) — `act show` text mode hides description and commit_marker.
- act-c26a (p=1) — `act create --blocked-by` + composed `act_file_blocker` MCP tool with atomic rollback. Marquee remaining v0.2 work; recommend a focused review-then-implement cycle when you're back rather than a sleepy worktree run.
- 8 p=2 issues: dep direction display, title-`--` misparse, `--include-ops` no-op, IsValidID cap, HLC tiebreak, applyClaim race, deps type mismatch, close-reason cap, log --summary, act mine/ready --mine.
- 2 p=3: doctor SQL cleanup, UX polish nits bundle.

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

**Updated read: ready for an alpha trial.** All three named showstoppers (claim+rebase, ambiguous-prefix exit, prefix matching) closed. Auto-commit messages canonical. Reviewer's high-severity findings either landed or filed for follow-up.

**Reasonable next step:** drop `.act/` into a small repo of yours (the knowledge base, an old personal-tool repo) and have an agent file + work + close issues there. Watch the trace. Anything broken becomes a follow-up here. The dogfood loop has shipped 17 fixes from the same kind of trial run on this repo, so the pattern is proven.

**Full "drop into any repo" readiness** still wants: a global Claude Code skill that triggers on the presence of `.act/` and contains the workflow patterns; a `brew install` tap or `curl … | sh` so installation isn't `go build`; a published GitHub Release (currently draft); arguably also act-c26a (composed primitives) since `act_file_blocker` is the workflow shape agents most want for "file a bug, link it" moments. Not blockers for an alpha but they're the difference between alpha and "use this anywhere."

## Operational notes

- Three worktrees from earlier merged agents were cleaned up at session end (branches deleted local + remote). Only one worktree currently live: the d3a5 agent's.
- Two background agents finished and reported back during the session (UX-eval, claude-code-guide); their full reports are in the conversation transcript. The actionable bits are filed as act issues or reflected here.
- `bin/act` is gitignored; rebuild with `go build -o bin/act ./cmd/act` if missing.
