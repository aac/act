---
type: memo
ticket: act-bcce95
status: completed
pinned_commit: e1663b0a26e367052b436b53d1fd6d60ff44e6f4
date: 2026-05-28
---

# Pre-release audit (act-bcce95)

## Outcome

**Three independent reviewers completed structured reviews against the pinned commit. 17 findings filed as derivative tickets — 5 priority-1 (release-gating), 9 priority-2, 3 priority-3. One additional priority-1 process-bug surfaced about the dispatch infrastructure itself (worktree auto-reap).**

**Top-level recommendation: NOT ready for the public flip until the 5 priority-1 findings are resolved.**

## Pinned audit commit

`e1663b0a26e367052b436b53d1fd6d60ff44e6f4`

## How this audit was run (and the prior halt)

The first attempt dispatched a meta-orchestration worker that was expected to spawn three reviewer subagents. That worker correctly halted: the `Agent` tool is not available at the dispatched-worker level — only at the orchestrator level. The worker filed `act-a09752` capturing the tool gap, plus a halt memo (this file's prior contents) recommending one of three unblock paths.

This pass took the recommended path: the orchestrator dispatched the three reviewers directly as parallel worker units, then synthesized.

During the audit run, a second process-level finding surfaced (`act-3e7a98`): when a worker's only outputs are gitignored (`.act/ops/` writes via `act create` / `act dep add`), the harness sees "no host-repo changes" and auto-reaps the worktree before the orchestrator can run `act harvest`. All three reviewers hit this; their tickets were re-created from the conversation-preserved reports. The pattern needs a fix before any future audit-shaped dispatch.

## Reviewer A — code-quality / correctness

Read `cmd/act/`, `internal/cli/`, `internal/gitops/`, `internal/ops/`.

**Findings (0 P1, 3 P2, 2 P3):**

- `act-c58aaa` (P2) — `CloseOptions.Reason` comment says >4096 bytes rejected; actual constant is 500. Misleading API contract.
- `act-acdf5d` (P2) — `gitops.CheckIgnored` bypasses runner seam + gitDir override (latent `act-784b` class).
- `act-f64d6e` (P2) — `claimGitOps.runGit` (update.go claim staging) bypasses gitDir override + runner seam. Same class.
- `act-6d7001` (P3) — `pushAttemptCounter` / `shallowFaultCounter` not atomic; `-race` flake risk.
- `act-d31605` (P3) — `harvest.go` calls `Commit()` directly, bypassing the `AutoPushAfterCommitToBranch` invariant honored everywhere else.

**Reviewer A verdict:** No priority-1 release-gaters. Land the three P2s before public flip; P3s are tracked but not blocking.

## Reviewer B — public-readiness / docs

Read `README.md`, selected `docs/`, `cmd/act/help.go`, `CLAUDE.md`, the global skill at `~/.claude/skills/act/SKILL.md`, and the agent-tools-release KB project.

**Findings (3 P1, 3 P2, 1 P3):**

- **`act-9b5339` (P1)** — `act compact --issue <id>` documented in `helpOverview` + `helpOpsModel`, but the subcommand does not exist; invocation returns `unknown subcommand`.
- **`act-91b10f` (P1)** — `go install github.com/aac/act/cmd/act@latest` is the primary install path; must verify `github.com/aac/act` is public before flip, or every install attempt fails.
- **`act-9dff9b` (P1)** — `LICENSE` file absent at repo root. Default legal posture is all-rights-reserved.
- `act-3a4321` (P2) — Internal tracker IDs (`act-e6a5`, `act-e31aa1`, `act-c4c5`, `act-f9a0`, `act-0852da`, "Phase 2 ticket 7") leak verbatim into user-visible `act help` output.
- `act-387e01` (P2) — `act help errors` EXIT CODES block omits exit 3 (`issue_not_found`) and exit 4 (`push_exhausted`, `remote_unreachable`).
- `act-983139` (P2) — `migrate-to-nested` missing from `act help` subcommand listing.
- `act-9d2340` (P3) — `docs/` lacks an index distinguishing authoritative docs from working papers (~20 process artifacts interleaved).

**Reviewer B verdict:** Not ready for public flip without the three P1s resolved.

## Reviewer C — test coverage / verification discipline

Read `internal/cli/docclaim_test.go`, `cmd/act/docclaim_test.go`, `internal/cli/docs_sweep_test.go`, sampled tests across both packages, ran `go test ./...`.

`go test ./...`: all packages pass. `internal/cli` runs subprocess end-to-end tests (159s, acceptable). `internal/cli` coverage 72.4%; `cmd/act` 9.3% (thin but delegates to `internal/cli`). No flaky timeouts.

**Findings (2 P1, 3 P2):**

- **`act-1849a6` (P1)** — `--no-doctor` flag (user-visible, documented in help.go) tested only at the `RunClose()` internal API; no `TestDocClaim_*`, no sweep-registry entry. Exact prior-bug pattern of `act-6fca` / `act-ac52`.
- **`act-2af8c7` (P1)** — `claim_lost` / last-write-wins claim documented in `act help errors` + spec-v2 §7.4 ("asserted across 100 iterations") but `TestConcurrentClaimRace` carries a permanent `t.Skip("Phase 1...")` with no single-machine replacement. Actual subprocess-boundary assertion count: zero.
- `act-54462e` (P2) — 500-byte `--reason` cap correctly tested at subprocess boundary but not named `TestDocClaim_*` and not in sweep registry.
- `act-ddd458` (P2) — `--include-ops` flag claim tested only via `RunShow()` internal call; no boundary assertion.
- `act-915a88` (P2) — `TestDocClaim_Errors_PushExhausted` / `_RemoteUnreachable` check constant equality and spec prose rather than binary behavior; behavioral test (`TestActClose_PushExhausted_ReturnsEnvelope`) exists but is unregistered.

**Reviewer C verdict:** Not release-ready. The two P1 gaps are the same failure class that produced `act-6fca` and `act-ac52`; the concurrent-claim gap is additionally an unmet spec §7.4 commitment.

## What's working well (aggregated, deduplicated)

1. **`op.ProbeAndWrite` atomic-rename + fsync ordering** (`internal/op/filename.go:atomicWrite`) is correct (Write → Sync → Close → Rename). Common bug class, gotten right here. Must not regress.
2. **Type-system enforcement of `ActGitOps` vs `HostGitOps`** (`internal/gitops/gitops.go`): write methods only callable through `*ActGitOps`. "act never writes to the host repo" is a compile-time guarantee, not a convention. Load-bearing.
3. **Three-way push-error lattice** (`*PushExhaustedError` / `ErrFetchFailed` / generic) maps to distinct exit codes + envelope codes via correct `errors.As`/`errors.Is` chains. Callers can take targeted action.
4. **Two-list rollback pattern in `WriteOpsAndAutoCommit`** (`written` vs `staged`) prevents spurious `git restore --staged` on untracked paths. Comment references the bug that motivated it (`act-c22b`).
5. **Doc-claim sweep registry** (`internal/cli/docs_sweep_test.go`): bidirectional enforcement, 60+ tracked claims, `crossRepoDocClaimTests` escape hatch. The mechanism that prevented `act-6fca` and `act-ac52`. Reviewers A, B, and C all flag this as load-bearing — do not weaken it. The new P1/P2 test findings are about *gaps* in coverage, not about weakening the discipline itself.
6. **`TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves`** is the canonical pattern: drives `actBinary show <prefix>` as a subprocess, asserts on exit code + stdout JSON. Use this shape for every future doc claim.
7. **`TestDocClaim_CWDRobustness_DoctorFromInsideActDir`** asserts at exactly the right boundary — `cd`s into `.act/`, runs the binary, checks the user-visible sentinel string. Mirrors the failure mode that motivated the fix.
8. **README is publication-quality.** Elevator pitch, storage layout, canonical workflow example, Beads credit — all clear to a stranger.
9. **`act help` canonical loop is correctly written.** Numbered, accurate commit-marker instructions, realistic worked example.
10. **`docs/spec-v2.md` and `docs/migration-runbook.md` are production-grade.** Internally consistent, accurate against code, with go/no-go verification commands.

## Release readiness — recommendation

**Hold the public flip until five priority-1 findings land:**

| ID | Area | Blocker |
|---|---|---|
| `act-9b5339` | docs / help | `act compact` doc claim mismatch — agents/users hit "unknown subcommand" immediately |
| `act-91b10f` | distribution | Verify `github.com/aac/act` is public before flip OR change install instructions |
| `act-9dff9b` | legal | `LICENSE` file present at repo root |
| `act-1849a6` | test discipline | `TestDocClaim_*` for `--no-doctor` at subprocess boundary |
| `act-2af8c7` | test discipline | Subprocess-boundary concurrent-claim test replacing the permanent `t.Skip` |

The nine priority-2 findings (gitDir-bypass class, exit-code doc gaps, ticket-ID leak, registration drift) should land soon but are not flip-blockers. The three priority-3s are polish.

Additionally, **`act-3e7a98` (P1, process)** — the worktree auto-reap problem — needs a fix before any future audit-shaped dispatch can be trusted to preserve its work.

## Acceptance criteria status

- "≥3 independent reviewer agents have completed structured reviews against current HEAD with pinned commit hashes and >70% confidence findings" — **MET**.
- "All findings filed as derivative act issues, linked from this issue's close reason" — **MET** (17 substantive findings + 1 dispatch-infrastructure finding + 1 process-bug finding, all wired `relates → act-bcce95`).
- "'what's working well' closing section captured from each reviewer for forward reference" — **MET** (aggregated above; per-reviewer detail in conversation transcript).
