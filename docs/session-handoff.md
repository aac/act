# Session handoff — 2026-05-10 (night)

Four close cycles. Bootstrap pitch documented (with one halt: the repo needs to flip public for the pitch to actually work for friends).

> **Quick read:** Shipped act-6051 (README + `act help` lead with `go install github.com/aac/act/cmd/act@latest`), act-c83a (hook stderr now surfaces in close/create/update/reopen error envelopes), act-c22b (rollback unstages only successfully-staged paths, with regression test), and act-9c8c (`act show` lists work commits attributed via the `(act-XXXX)` marker). Verification on act-6051 surfaced a precondition: the canonical pitch only works for friends once the aac/act repo is public + a fresh release tag is cut. Filed as **act-2204** (p=1) with explicit verification AC. Andrew said in conversation "maybe there's no reason for it to stay private right now" — that decision is the next gate before sharing.

## What landed this session

- **act-6051 closed (0a006ed).** README rewritten: leads with `go install github.com/aac/act/cmd/act@latest && act init`. Status section refreshed (no longer "in progress"). Brew tap, prebuilt release, curl installer documented as alternates with their tradeoffs, not equally-promoted paths. `act help` got a matching `GETTING STARTED` section between WHAT THIS IS and THE CANONICAL WORK LOOP. Test (`TestRunHelpDefault`) asserts the section is present so README + help can't silently drift. AC #2 (end-to-end test from a fresh GOMODCACHE without auth) deferred to act-2204 — see "Bootstrap verification finding" below.
- **act-c83a closed (facaf78).** New `HookFailureDetails` helper in `internal/cli/errors.go` extracts `*hooks.HookFailedError` into (human Message with last 10 stderr lines inline, Details map with full `hook_stderr` + `hook_exit_code` + `hook_truncated`). Wired into close.go's hook_failed path and create/update/reopen's write_failed split — hook failures now surface as `error: "hook_failed"` not `write_failed`, with the captured stderr available to JSON consumers. 7 new tests cover helper unit behavior + integration on close + create.
- **act-c22b closed (7c95ead).** `WriteOpsAndAutoCommit` now tracks a `staged []string` slice separately from `written []string`; rollback unstages only paths that successfully passed `StageOpFile`. `runUnstage` indirected through a swappable `runUnstageFn` for testability. Two regression tests: commit-failure rollback unstages exactly the staged paths; ProbeAndWrite-failure on op 2 of 2 (triggered via shard-collision on a regular file masquerading as a directory) results in zero unstage calls — would have failed pre-fix.
- **act-9c8c closed (ab70484).** New `gitops.WorkCommitsForIssue(prefix4, limit)` runs `git log --all --fixed-strings --grep='(act-<prefix4>'` and returns `[]WorkCommit{SHA, Subject, AuthorDate}`. `RunShow` populates `ShowResult.Commits` best-effort (git failure → empty, not error). Human renderer appends a `commits:` block when non-empty; `ShowJSON` always emits a `commits` key (empty array when none) so MCP consumers can rely on the key. Verified on this repo: `act show act-c83a` now displays facaf78 + the act-op claim/create commits inline.

## Bootstrap verification finding (act-2204)

The canonical pitch in README and `act help` is `go install github.com/aac/act/cmd/act@latest`. Verification done two ways:

1. **From a fresh GOMODCACHE without auth** (what a friend's agent would have): FAILS. `sum.golang.org/lookup/github.com/aac/act@v0.1.0: 404 Not Found` because the proxy never mirrored a private module; falls back to `git ls-remote` which fails on the private repo with `terminal prompts disabled`.
2. **With `GOPRIVATE=github.com/aac` + Andrew's git auth**: SUCCEEDS but installs `v0.1.0` from the proxy cache, which is **192 commits behind HEAD** as of c8ae75f. The cached binary is functional for `act init` / `create` / `ready` / `show` but missing every fix landed since (including act-8277's hook-resolver fix, the per_session bundling, all the act-c26a/c83a/c22b/9c8c work).

Conclusion: the README pitch is correct in shape, but actually working for a fresh agent in someone else's repo requires (a) flipping aac/act public so sum.golang.org can mirror the module, then (b) cutting a fresh release tag at or near HEAD so `@latest` resolves to current code. Both captured in act-2204 with verification AC: "From a fresh GOMODCACHE with no GOPRIVATE / no git auth, `go install …@latest` completes successfully."

Andrew's stance in conversation: "Do what you can while it's private, and do any other fixes we'd want to do before handing it to someone else. First thing we'll do is use it in another project on my machine. Unless there's no reason for it to stay private right now (which maybe there isn't). Making it public would let us do some stuff from CC on the web." Repo history is clean (no secrets in op-log JSON or docs).

## Where things stand

- Backlog: 16 ready (act-9c8c, act-c83a, act-c22b, act-6051 closed; act-2204 added).
- Top of queue:
  - **act-2204** (p=1, new) — flip aac/act public + cut fresh release tag. Andrew's call, blocking the canonical-pitch verification.
  - **act-ff5c** (p=1) — doc-drift prevention process. Brainstorm-first; act-8277 (this session's predecessor's discovery) is exhibit A, the new `TestRunHelpDefault` assertion that the GETTING STARTED section exists is exhibit B.
  - **act-8416** (p=1) — Cowork integration. Needs external context.
  - **act-4fe6** (p=1) — CC Web integration. Needs external context. Would benefit from act-2204 landing first (fewer install-path workarounds to document).
  - **act-b90e** (p=2) — version-control the act skill file. Untracked at `~/.claude/skills/act/` — relevant once we share, since the skill is half the install story for friends' agents.
  - **act-e6a5** (p=2) — brew tap / curl installer. Currently documented as alternates; lower urgency now that go install is the canonical path.
- All worktrees clean. CI green on origin/main (act-9c8c push: ab70484).
- Two issues filed mid-session: act-2204 (this session) and act-c83a / act-c22b (last session, both closed this one).

## What to look at first when resuming

1. **Decide on act-2204.** This is the gate before any external sharing. Two questions: (a) Is there any reason aac/act should stay private? Conversation suggested "maybe there isn't." History review found nothing sensitive. (b) Once flipped, cut a fresh release tag — `git tag v0.2.0 && git push --tags` plus publishing the existing v0.1.0 draft release should be enough; sum.golang.org will mirror once the repo is public.
2. **act-b90e is more important now than its p=2 suggests.** The README + `act help` pitch lands the binary, but the skill at `~/.claude/skills/act/SKILL.md` does the canonical-loop heavy lifting in agent sessions. If a friend's agent installs the binary but the skill doesn't auto-activate (because it's not version-controlled and doesn't ship with anything), the install-and-go promise breaks. Probably worth bumping to p=1 before sharing.
3. **act-ff5c brainstorm.** The handoff before this one already framed the bar: "would this have caught act-8277 before merge?" The two doc-test patterns this session demonstrated (the `TestRunHelpDefault` assertion that GETTING STARTED is present, and the act-8277 predecessor's `TestResolveHookMatchesDocs`) are concrete examples of what good drift-prevention tests look like — the brainstorm should generalize from those.
4. **act-8416 / act-4fe6** when ready to expand beyond Andrew's machine. Would benefit from act-2204 landing first.

## Sharing readiness (Sasank / Corey / Andrew Widdowson)

Same conclusion as last session, sharpened by the verification finding:

- **First dogfood is Andrew on another of his own projects** — works today via `GOPRIVATE=github.com/aac/act go install …@latest` (gets stale v0.1.0) OR `git clone && go install ./cmd/act` (gets HEAD).
- **For friends' agents**: blocked on act-2204. Once that lands, the README pitch works as written.
- **Companion concern**: the `act` skill needs to be findable by a friend's agent (act-b90e). The README mentions the skill auto-activates "from a Claude Code session" — true on Andrew's machine where the skill is installed, untrue on someone else's machine until act-b90e lands and the skill is published somewhere agents can pull.

## Operational notes

- `bin/act` current as of ab70484.
- `act show <id>` now displays both work commits (the agent's `(act-XXXX)`-tagged commits) and act-op commits (claim / create / close auto-commits) inline. Useful: scanning `act show` post-close shows the full git surface for an issue at a glance.
- Hook failures now surface stderr — running into a `gofmt drift` or test failure during close shows the trailing 10 lines of the hook's stderr in the error message instead of just `hook exited 1`.
- `WriteOpsAndAutoCommit` rollback no longer redundantly unstages never-staged files. No user-visible change today (the spurious stderr was already suppressed) but the structural fix matters if anyone ever wires `cmd.Stderr` through the runner.
- All session work in `main` (no worktrees). Sub-agents not used — work was tightly coupled around `internal/cli/`.
