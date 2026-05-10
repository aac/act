# Session handoff — 2026-05-10 (afternoon)

Andrew working through the commit-noise discussion → ended by landing act-a659 (close stages into work commit). Previous handoff was stale; overnight agents had closed 17 issues including all four p=1s I'd called out.

> **Quick read:** act-a659 shipped. Typical task lifecycle now produces **2 git commits** (claim + work-with-close) instead of 3, on per_session repos. Code reviewer caught one real bug (incomplete `--push` rollback leaving the close op file on disk) — fixed before commit. Reviewer's "what's working well" findings worth preserving: HasNonActChanges timing, hook-runs-in-both-paths invariant, per_op untouched, CLAUDE.md+docs accurate. Session safely clearable.

## What landed this session

- **act-a659 closed (commit 89b7ad4).** `act close` under per_session detects non-`.act` working-tree changes; if present, stages the close op and waits for the agent's next `git commit -am` to subsume it. Clean trees still standalone-commit. `--push` errors with full rollback when staged. New tests: `TestPerSession_CloseStagesIntoWorkCommit`, `TestPerSession_ClosePushErrorsWhenStaged`, `TestPerSession_NoCodeCloseCommitsStandalone`. CloseResult JSON gained `staged_for_commit` and `commit_marker`.
- **act-9c8c filed (p=2).** `act show <id>` should display related work commits via `git log --grep=(act-XXXX)`. Read-side ergonomic. Becomes more valuable now that work commits are the primary close-carrier under act-a659.
- **CLAUDE.md updated.** New versioning-rationale entry documents the close-then-commit ordering, the `--push` restriction, the JSON shape, and the no-code fallback. Loop ordering clarified.
- **`act help workflow` updated.** Canonical loop now teaches close → commit → push (was commit → close → push). Example session reflects the new flow with the staged-hint output.
- **`docs/commit-noise-design.md` updated.** Renamed `BEADS_MODE` → `ACT_MODE` (copy-paste artifact). Appended a post-implementation section explaining what act-728d shipped vs what act-a659 fixes.

## Reviewer findings preserved

The code-reviewer agent (lightweight, >70% confidence filter) returned one critical and one important finding plus a strong "what's working well" section. Both findings were fixed before commit:

1. *(fixed)* `--push` error path didn't `os.Remove` the close op file — issue would fold as closed in the op-log while the commit failed. Test was strengthened to assert the rollback (close op absent + retry succeeds).
2. *(fixed)* `act help workflow`'s `--push` recovery instruction implied the close stays staged after error; updated to match actual behavior (full rollback, re-run after work commit).

What's working well (do NOT regress):
- `HasNonActChanges` is called *after* `.act/` files are staged but *before* the commit decision, so staged act files correctly count as "act-only."
- The pre-close hook runs in both the staged and standalone branches; `.act/hooks/close` gates the close decision regardless of which path fires.
- `per_op` strategy is fully untouched — no behavior bleed; existing regression tests pass.
- `TestPerSession_CloseStagesIntoWorkCommit` actually executes the agent's `git commit` (via `runOut`), which is the right level of integration; pure-mock would have missed bugs.

## Where things stand

- 2 unpushed commits at session end? **No** — pushed clean. `git log origin/main..HEAD` is empty.
- Backlog: 17 ready issues. Top of queue (p=1):
  - act-6051 — canonical bootstrap (one command to get act working in a new project)
  - act-ff5c — doc-drift prevention process
  - act-75fd — evaluate act vs alternatives (the beads comparison Andrew parked earlier this session — explicitly deferred until act-a659 landed; it now has)
  - act-8416 / act-4fe6 — make act available in Cowork / CC Web
  - act-c26a — `act create --blocked-by` + composed `act_file_blocker` MCP tool (marquee remaining v0.2)
- Submodule research: act-8d67 (p=2, open) captures Andrew's "abstract submodule UX" question. Description has the A-E options analysis. Not yet worked.

## What to look at first when resuming

1. **act-75fd (beads comparison).** Andrew parked this earlier this session pending act-a659. Now unblocked. Worth dispatching to a worktree agent — independent, well-scoped, result feeds the noise-design doc and the multi-env analysis.
2. **act-c26a (composed primitives).** Marquee remaining v0.2 work. Recommend a focused review-then-implement cycle, not a sleepy worktree run.
3. **act-6051 (canonical bootstrap).** Adoption-blocker for trying act in another repo. Touches install, init, the `.gitignore` hook (act-2c7d already auto-commits .act/), and the doc story.
4. **Verify act-9c8c is right scope.** Filed at p=2; could arguably be p=1 now that work commits ARE the close commits under act-a659. Re-read its description before touching.

## Known stale areas worth cleaning

- The `bundle_strategy` field has two values (`per_op`, `per_session`) and the noise-design doc's "what's still uncertain" section asks whether `per_op` should be deprecated. Worth deciding once another repo has been on `per_session+act-a659` for a while.
- The `(act-XXXX)` marker in commit messages is asserted by `commit_format_test.go` and `bundle_test.go` independently — no consolidated test for "every write commit has a doctor-greppable marker." Possible follow-up if act-ff5c (doc-drift prevention) touches this area.

## Operational notes

- `bin/act` is current as of this session's commit (89b7ad4). Rebuild with `go build -o bin/act ./cmd/act` if missing.
- `.act/hooks/close` runs gofmt + vet + tests on every close; hook fires in both staged and standalone branches under act-a659.
- No live worktrees at session end.
- This session ran in `main` (no worktree); all work was on `cmd/act/`, `internal/cli/`, `internal/gitops/`, `internal/config/`, plus docs. CLAUDE.md continues the "default serial sub-agents in this repo" rule — held this session.
