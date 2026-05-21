# Session handoff — 2026-05-21 (afternoon wrap)

Three sessions ran 2026-05-21. This file is updated chronologically — most recent wrap on top.

## What shipped 2026-05-21

- **`act-4094c6`** — host pre-commit hook now permits staged `.act/*` deletions (uses `git diff --cached --diff-filter=d`). `commitHostMigrateChanges` drops `--no-verify` so the migration commit actually runs through the hook. Regression test simulates "hook already installed on the branch we're carrying the migration to," asserts the commit succeeds without plumbing or `--no-verify`. Docs-sweep registry entry pins the "permits deletions" claim. Commit `6a05287`, pushed.
- **`act-2d98`** — colon-bearing op filenames closed in a sibling session. Forward fix was already in place (act-2f3d); this close shipped `tools/migrate-ntfs-safe-op-filenames.sh` (host commit `3c54af8`). Side-effect close: `act-487a` bumped the close-hook test timeout 180s→300s (nested commit `6ff1c81`).
- **Local binary refresh** — `go install ./cmd/act` ran from this repo, replacing the May-16 build at `/Users/andrewcove/go/bin/act` (11.3MB → 11.8MB). Third session's recovery work in another repo (migration just-ran, old binary still on PATH) depended on this. Their half-landed `act-9329` op file is colon-bearing and uncommitted in their nested `.act/.git`; they have the rename-vs-recreate recovery paths from me.
- **Tickets filed (CLI dep-surface review)** — `act-00e5cc` (surface `blocks`/`blocked_by` arrays in `act show --json`), `act-5918c7` (spec-v2.md dep schema drifted), `act-2e1070` (deferred subcommand idea, blocked-by `act-00e5cc`, with explicit trigger condition). All from a two-reviewer design pass after a sibling session misread the `deps` JSON shape.
- **`act-0852da` filed + sub-agent dispatched** — `cmd/act/findRepoRoot` doesn't skip nested `.act/.git`, so any `act` command from inside `.act/` misresolves to the nested repo and returns "no act state." Internal resolver (`FindHostRepoRoot` in `internal/gitops/resolver.go`, from act-9e8c) already exists; was supposed to be wired through in act-37f7 and got missed. Sonnet sub-agent running in worktree to do the mechanical migration + boundary test.

## Cross-repo migration cleanup (still queued)

`ask-25aa` parks the run-the-cleanup-script-across-16-repos prompt at `~/Workspace/migrate-act-projects-prompt.md`. Inventory of legacy colon files inlined: financial has 864, ask 211, dispatch 3, others between. Trigger condition: all other Claude sessions in those repos idle first.

## Carryforward 2026-05-20

The previous handoff (2026-05-19 morning) covered Phase 2 plan v2 just becoming plan-ready. **Phase 2 has since shipped end-to-end via an autonomous orchestrate run.** A separate wrap session 2026-05-20 turned the operator-bound publishing tickets into a properly-shaped pre-public-release backlog after Andrew weighed in via a `/poke` free-text response.

## What shipped since the 2026-05-19 morning handoff

Autonomous orchestrate run (orchestrator log archived in the prior chat; commit range ~`8b5c360..9ce01fa`):

- **Phase 2 in full** — design + reviews + plan + reviews + 13 implementation tickets + dogfood pass.
- **Five dogfood-derived bug fixes** — NTFS-safe filenames (×3), bootstrap hook copy, git stderr leak, remote-enable severity, post-receive bare-act.
- **Plumbing** — schema migration auto-add, dispatch-test no-state guard, `bundle_strategy` cleanup, `.ask/` decoupling.
- **Polish layer** — assignee + `claimed_at` columns in `act ready`, `--full` on `act show`, `--summary` on `act log`, `--branch <ref>` for write ops, `--no-doctor` opt-out + commit-marker check on close, `--since` / `--by-issue` / `--type` log filters.
- **Bugfixes** — `applyClaim` closed-status race, FTS5 search query quoting, `checkIndexSchema` query, dep-direction display strings, `act close --reason` length-upfront validation, redact removal.
- **Infra** — hook timeout 120→300s, gofmt scan excludes sibling worktrees, E2E test seam wired.
- **Docs** — README, migration runbook, skill worker-protocol section, branch-discovery + stale-claim recovery in spec-v2.
- **Doc-claim sweep auto-extract** — `cmd/docclaim-sweep` first-cut analyzer (act-2415, `ae457c9`). Closed mid-run; the orchestrator's wrap text mis-flagged it as deferred.

After that run wrapped (`9ce01fa`), HEAD picked up:

- `08be000` — `chore: gitignore .ask/` (this session, `ask init` ran in this repo).
- The four new act issues filed this evening land as nested-repo op-commits in `.act/.git`, not host commits.

## What today's wrap session did

Andrew's `/poke` free-text response (logged in the poke state.json submissions, since torn down) reframed the publishing work: act ships as part of the **agent-tools-release joint launch** (KB project: `~/Workspace/knowledge/projects/agent-tools-release.md`); publishing repos can happen any time but the launch announcement is the coordinated event. Real pre-flip blockers:

1. **Reviewer audit** on repo state — quality, dotted i's, ready-for-strangers read.
2. **Commit-history cleanup decision** — is the log too gross for public consumption, and what (if anything) to do about it.
3. **Live-fire test sweep** across every local act-using project — survive a release cycle.
4. **Brew vs curl-installer brief** — Andrew is agnostic, wants a comparison.

Action taken in the wrap:

- Filed `act-bcce95` (audit), `act-cb9750` (history cleanup), `act-b4288f` (live-fire), `act-2b65b0` (distribution brief).
- Wired blocks: `act-2204` (publish) blocked by audit + history-cleanup + live-fire. `act-e6a5` (brew tap) additionally blocked by the distribution brief. `act-e6a5`, `act-4fe6` (CC Web), `act-8416` (Cowork) also block on `act-2204`.
- Closed `ask-cb9c` and `ask-011a` as premature framing — they assumed publishing was the load-bearing operator step, when in fact the audit/cleanup/live-fire work is what's load-bearing right now. Refile a publish ask once those land with concrete "we're ready" framing.

## Current state of `act ready`

Top of the queue is now the four pre-release blockers:

1. `act-b4288f` — Live-fire test sweep across local act-using projects (P1)
2. `act-cb9750` — Commit-history cleanup investigation (P1)
3. `act-bcce95` — Reviewer audit pass on repo state (P1)
4. `act-2b65b0` — Distribution comparison brief (P2)

Plus pre-existing work that didn't move this session: see "Open threads" below.

## Open threads for next session

Carried forward from the 2026-05-19 morning handoff (still open, not on this session's plate):

1. **`act-784b`** — auto-commit regression: nested-repo case fails when host gitignores `.act/`. Fix `8850ceb` (gitops: nested-repo auto-commit targets .act/.git working tree) IS on main as of 2026-05-21 verification — the financial-repo session's own writes via the post-refresh binary all landed in nested `.act/.git`, which couldn't happen if the fix were missing. The act-784b issue itself is still status=in_progress in the tracker; close it next time someone's in the loop.
2. **`act-993b93`** — dep dispatch tests fail under no-state guard. Status: in_progress, claimed 2026-05-19. Commit `6fb554a` made an attempt; verify tests pass.
3. **`act-2d98`** — op-file names with colons fail on Windows. **Closed 2026-05-21** in a sibling session. Forward fix was already in place (act-2f3d); this close shipped the cross-repo cleanup script `tools/migrate-ntfs-safe-op-filenames.sh` (host commit `3c54af8`, pushed). The cleanup pass across 16 adopting host-tracked repos is queued — paste-ready prompt at `~/Workspace/migrate-act-projects-prompt.md`, surfaced as **`ask-25aa`** ("needs all other Claude sessions idle first"). Inventory inlined in the prompt; financial has 864 legacy files, ask 211, dispatch 3, others in between. Side-effect close: `act-487a` (close-hook `go test` timeout bumped 180s→300s, nested commit `6ff1c81`) — internal/cli grew past the original 180s ceiling and was tripping every close in this env.
4. **Ext-dep-should-actually-gate bug** — discovered 2026-05-19 mid-day but never filed (search confirms no ticket). Briefcraft's design ran despite an open `ext-add` dep on `arc-ask-9317`. File when somebody picks this back up.

Closed since the previous handoff:

- `act-b77a80` (Phase 1.5 umbrella) — done, closed 2026-05-19 07:06.
- All Phase 2 implementation tickets — landed in the autonomous run.

## Cross-references

- Previous handoff: at commit prior to `08be000`.
- KB project for the broader launch shape: `~/Workspace/knowledge/projects/agent-tools-release.md`.
- Phase 2 plan v2 (now executed): `docs/coordination-plane-phase2-plan.md`.
- Codex integration plan (release-blocker upstream of all four tools): `~/Workspace/codex-agent-tools-integration-plan.md`.
- Doc-discipline rule for the audit ticket to validate against: `CLAUDE.md` "Documentation discipline" + `internal/cli/docs_sweep_test.go`.
