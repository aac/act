# Session handoff — 2026-05-10 (late evening)

Two close cycles + one root-cause-of-CI-red fix. Bootstrap path settled in conversation: `go install` is the canonical pitch.

> **Quick read:** Shipped act-75fd (eval refresh), act-c26a (`--blocked-by` + `act_file_blocker`), and act-8277 (hooks-never-fire root cause + gofmt drift). CI was red across 5+ runs because `.act/hooks/close` had been a silent no-op since it was created — resolver looked for `post-close`, file was named `close`. Bug fixed; gate now actually catches drift locally before push. Three follow-ups filed (act-c22b, act-c83a, plus act-9c8c still open from afternoon). Andrew settled the bootstrap direction: go install (uvx-style — one command from a fresh agent session). Ready to share with Sasank / Corey / Andrew Widdowson once act-6051 implements the README pitch + verifies the public module path works.

## What landed this session

- **act-75fd closed (a52bdb7).** docs/act-evaluation.md refreshed: per_session + close-stages-into-work-commit approximate Dolt's "transaction = one commit" property in plain git; the "is per-op-commit load-bearing?" question (act-6018 once asked) is resolved (hideable).
- **act-c26a closed (b4610f6).** Shipped `act create --blocked-by <id>` (repeatable, dedups) + `act_file_blocker` MCP composed tool. Single atomic commit via WriteOpsAndAutoCommit with rollback. 12 new tests, tools count 15→16. AC #4 deviation documented: `--block-parent` NOT implemented per Andrew's "single choice, not flags" design call; workflow A continues via act_block after create.
- **act-8277 closed (2f8ddd2).** Two-cluster root cause fix for the persistent CI red:
  1. `internal/hooks/hooks.go` ResolveHook map renamed `post-<op>` → bare `<op>` to match every doc + the actual `.act/hooks/close` file. Silently no-op'd hooks now actually fire.
  2. hookTimeout bumped 5s → 120s. Original 5s was sized for quick lints; the act repo's close gate runs `gofmt + vet + go test ./...` (~50s). Even with the resolver fixed, 5s would have timed out every gate.
  3. gofmt-cleaned close.go + config.go (the drift CI was failing on — would have been caught locally if the hook had ever fired).
  Verified end-to-end: introduce deliberate drift → close exits 1 → close op rolled back. CI green on the fix commit.
- **Filed follow-ups:** act-c22b (WriteOpsAndAutoCommit rollback unstage-noise, reviewer-derived from c26a), act-c83a (HookFailedError.Error() drops the captured StderrTail; users see "hook exited 1" with no signal about what failed). act-9c8c from the prior session is still open.

## Conclusions worth preserving

**Log noise question — practically settled.** Two-commit-per-issue lifecycle on per_session repos approximates the Dolt commit pattern in plain git. The act-evaluation doc captures this. Remaining open question (deprecate per_op outright?) genuinely needs another repo's data — not more thinking from inside act.

**`--blocked-by` design call (act-c26a).** Workflow C reduces to one call cleanly; workflow A (file a blocker for current work) continues via act_block after create. Single flag with a single semantic; no --block-parent. Worth defending if a future reviewer flags drift from the original AC.

**Hook gate must actually run (act-8277).** Every close hook fires now. The `.act/hooks/close` script is the local pre-flight gate that CI duplicates; both being green is the contract. The gate caught zero of the recent close commits because of the resolver bug, which is why a six-month-old drift made it to main.

## Bootstrap decision (act-6051)

Conversation settled on `go install github.com/aac/act/cmd/act@latest` as the canonical pitch — the Go equivalent of the uvx pattern Simon Willison uses. One command from a fresh agent session lands `act` on PATH; from there, the act skill auto-activates and the agent can self-bootstrap by running `act help` / `act ready`. Brew tap (act-e6a5) and a curl installer stay as alternates, not the primary pitch.

Next session: implement the README pitch + verify the public Go module path actually works (`github.com/aac/act` — confirm the repo is public and `go install ...@latest` resolves cleanly from a fresh `$GOPATH`). Then act-6051 closes with the README documenting one-command install + `act init` to get a new repo going.

## Where things stand

- Backlog: 17 ready. Top of queue:
  - **act-6051** (p=1) — canonical bootstrap; direction settled, implementation is next session's first task.
  - **act-ff5c** (p=1) — doc-drift prevention process. act-8277 is exhibit A for why this matters; the test added (TestResolveHookMatchesDocs) is exhibit A for what the process should produce. Worth a brainstorming pass.
  - **act-8416 / act-4fe6** (p=1) — Cowork / CC Web integrations. Need external-system context.
  - **act-c83a** (p=2, new) — hook stderr surfacing. Trivial fix, clear regression test.
  - **act-c22b** (p=2, new) — rollback unstage noise. Trivial fix.
  - **act-9c8c** (p=2, carryover) — show work commits in `act show`. Smallest concrete win, ~30 min.
- All worktrees clean. CI green on origin/main.

## What to look at first when resuming

1. **act-6051 implementation.** Direction is decided; this is mechanical. Confirm public Go module path → README → close.
2. **act-c83a then act-c22b.** Both are small, both make the next CI / close cycle quieter. Quick wins to bundle.
3. **act-ff5c.** Brainstorm first. The doc-drift class spans resolver/doc mismatches (act-8277), unexercised invariants (the act-act-double-prefix bug Andrew mentioned earlier), and silent gate regressions. A doctor check + a test pattern + a CI guard is probably the shape. Don't over-engineer; the bar is "would this have caught act-8277 before merge?"
4. **act-9c8c** if there's time. Read-side, isolated, smallest discrete win.

## Sharing readiness (Sasank / Corey / Andrew Widdowson)

After act-6051 lands a working `go install` pitch:
- Yes for personal-repo alpha trial. Workflow loop survives cold-start; log noise resolved on per_session; architecture good for solo-to-small-multi-agent.
- Pitch shape: "go install github.com/aac/act/cmd/act@latest; cd <your repo>; act init; act create 'first task'" → done in 30 seconds.
- Ask them to share: what was their first friction point, what tripped a cold-start agent, what does the log look like after a week.
- The act-evaluation doc's "what changes the read next" line — "a real alpha trial in another repo" — is exactly what this enables.

## Operational notes

- `bin/act` current as of 2f8ddd2.
- `.act/hooks/close` now ACTUALLY runs on every close (act-8277). Be aware: introducing gofmt drift will block your closes locally now — that's the intended gate.
- Two test issues (act-2434, act-498a) created during act-8277 hook verification were tombstoned via `act delete`; this is the first session that exercised `act delete` deliberately, and it worked cleanly.
- The "default serial sub-agents in this repo" rule held. All work in `main` (no worktrees).
