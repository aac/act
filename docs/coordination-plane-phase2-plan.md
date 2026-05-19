# Coordination plane — Phase 2 implementation plan

**Status:** v1 draft, awaiting plan-review gate.
**Author:** drafted in session 2026-05-18, following the design brief at `docs/coordination-plane-phase2-design.md` v4 (commit `a7f1bd1`).
**Scope:** turn the design brief into a sequenced, scoped implementation. Eleven tickets across four phases of integration. Each ticket has acceptance criteria, test plan, and explicit dependencies.

## Plan structure

Four phases, sequenced by dependency. Within each phase, the named tickets can run in parallel under `/orchestrate` (provably disjoint files in most cases — collisions called out where they exist).

1. **Foundation** — config, hooks, the `act remote enable/disable/add-upstream` UX, doctor scaffolding for the new config keys. Everything else depends on this.
2. **Write path** — push-on-write, the fetch-rebase-push retry helper with reachability verification, slow-write warning, error envelope additions. The largest phase by code volume.
3. **Read path and sync** — TTL cache, `ACT_DISPATCH_MODE` bypass, `act remote sync` subcommand, post-receive hook wiring, write-path sync trigger.
4. **Bootstrap, harvest narrowing, doctor extensions, integration** — `act bootstrap-worker --from-remote`, harvest narrowed to fallback role, doctor's three-state reconciliation cases (a')/(c')/(f)/(g)/(h), `/orchestrate` workflow updates, end-to-end integration tests.

Total: ~3500-5000 lines of code + tests across the eleven tickets. Roughly 2-3 weeks of `/orchestrate` time at the dogfood pace we ran last week.

---

## Phase 1 — Foundation

### Ticket 1: `act remote enable / disable / add-upstream` + config keys

Foundational; everything else depends on it.

**Scope.** New subcommands and the config schema they manipulate.

- `act remote enable`: sets `receive.denyCurrentBranch=updateInstead` on `.act/.git`; sets `act.readCacheTTLSeconds=5`, `act.bootstrapTimeoutSeconds=30`, `act.fetchTimeoutSeconds=10`, `act.slowWriteThresholdMs=1000`, `act.upstreamDriftThresholdCommits=50`, `act.upstreamDriftThresholdSeconds=3600`; installs the `.act/.git/hooks/post-receive` skeleton (no-op body — Phase 3 fills it in); runs `act doctor` post-config to verify.
- `act remote disable`: unsets the above. Idempotent.
- `act remote add-upstream <url>`: adds `origin-upstream` remote pointing at the given URL; does an initial `git push origin-upstream <branch>`; refuses public-URL upstreams (warning, then error unless `--force-public`).

**Acceptance criteria.**

- `act remote enable && git config -f .act/.git/config receive.denyCurrentBranch` outputs `updateInstead`.
- `act remote disable && git config -f .act/.git/config --get receive.denyCurrentBranch` returns non-zero.
- `act remote add-upstream https://github.com/...` succeeds for a private repo and the initial push lands.
- `act remote add-upstream https://github.com/aac/public-thing` errors with `upstream_public`, exit 2. `--force-public` overrides.
- `act doctor` post-enable returns zero findings.

**Test plan.** Subcommand-level integration tests using temp dirs and a fixture `.act/.git`. Mock the GitHub remote with a local bare repo for `add-upstream` tests. Doctor invocation tested via `TestDocClaim_*` registry entry.

**Files touched.** `cmd/act/remote.go` (new), `cmd/act/main.go` (dispatcher entry), `internal/cli/remote.go` (new), `internal/config/config.go` (new config keys). `docs/spec-v2.md` (error-table addition for `upstream_public`).

---

## Phase 2 — Write path

These three tickets are tightly coupled; ticket 2 produces a helper that ticket 3 uses; ticket 4 is the doctor-side error-envelope discipline. Plan dispatches 2 first, then 3 and 4 in parallel.

### Ticket 2: push-contention retry helper

The reusable git-level helper. No CLI surface.

**Scope.** A `gitops.PushWithRetry(branch, opts) error` function that implements the v4 brief's "fetch, rebase, push, verify" loop:

1. `git push origin <branch>`.
2. On non-fast-forward: `git fetch origin <branch>`; `git rebase origin/<branch>`; goto 1.
3. On apparent success: `git fetch origin <branch>`; `git merge-base --is-ancestor <local-HEAD> origin/<branch>`. If non-ancestor, treat as silent rejection; goto 2.
4. On rebase failure with `shallow file has changed` or `unable to find common ancestor`: `git fetch --unshallow origin <branch>` once; goto 2.
5. After N (default 5) full retries with exponential backoff capped at 1s, return `ErrPushExhausted`.

**Acceptance criteria.**

- Two parallel processes pushing to the same bare repo: both eventually succeed, neither loses commits. Asserted by post-test inspection of `.act/ops/`.
- Simulated `updateInstead` dirty-tree silent rejection (test fixture dirties the receiving working tree mid-push): caller's retry loop detects it via reachability check and recovers within the N-retry cap.
- Simulated shallow-clone rebase failure (fixture creates a `--depth 1` clone and advances the remote beyond it): `--unshallow` fallback triggers; subsequent retries succeed.
- Exhausted retries return `ErrPushExhausted` with `Envelope.Code = push_exhausted` and `details.retry_count`.

**Test plan.** Concurrency tests using `t.Parallel` with two goroutines invoking the helper against a shared bare fixture. Shallow-clone failure simulated by setting a remote that refuses `--update-shallow` (custom `git daemon` config in test setup). Spec entries for `push_exhausted` and `remote_unreachable` codes go in the same commit.

**Files touched.** `internal/gitops/push_retry.go` (new), `internal/gitops/push_retry_test.go` (new), `internal/cli/errors.go` (two new constants), `docs/spec-v2.md` (error-table entries), `internal/cli/docs_sweep_test.go` (two `TestDocClaim_*` registry entries).

### Ticket 3: push-on-write integration in `actGitOps`

Wire ticket 2's helper into every `act` write path.

**Scope.**

- After every `actGitOps.Commit()` on a remote-configured project (`origin` is set), invoke `gitops.PushWithRetry`.
- On `ErrPushExhausted`, return the envelope to the caller. Write-side ops do not roll back the local commit — the op file exists locally and recovers via harvest if needed.
- Slow-write measurement: `actGitOps.Commit()` measures monotonic time between stage and commit. If duration > `act.slowWriteThresholdMs`, emit a warning to stderr and append a structured JSON line to `.act/.slow-writes` (rolling, capped at 100 entries — older entries pruned in-place).
- `--offline` flag on all write subcommands: commits locally, skips push, records "pending push" in a state file (`.act/.pending-pushes`) that the next non-offline write retries before its own push.

**Acceptance criteria.**

- `act create` on a remote-configured project pushes synchronously by default. `act log` on a peer worker's clone shows the new op after `act create` returns.
- `act create --offline` commits locally without pushing; subsequent `act create` (non-offline) flushes the prior queued push before its own.
- A 2-second sleep injected into the commit path (test-only fault injection) produces a stderr slow-write warning and a `.act/.slow-writes` entry.
- After 5 push retry exhaustion, `act close` returns `push_exhausted` envelope, exit 4.

**Test plan.** Integration tests with a fault-injected slow filesystem (using `bazil.org/fuse` mock or just a sleep injection point gated by env). Spec/test-discipline entries for the slow-write warning text claim.

**Files touched.** `internal/gitops/gitops.go` (actGitOps integration), `internal/cli/create.go`, `internal/cli/close.go`, `internal/cli/update.go`, `internal/cli/depadd.go`, `internal/cli/reopen.go`, `internal/cli/delete.go` (all write paths), `internal/cli/offline.go` (new, shared `--offline` flag handling). `docs/spec-v2.md` (offline-flag documentation, slow-write warning behavior). Bundle dispatcher conflict risk: high — all write paths touched. **Do not run in parallel with any other write-path-touching ticket.**

### Ticket 4: error envelope and spec-v2.md additions (test-discipline support)

Bundled into 2 and 3 above. Not a separate ticket — kept here for plan visibility. The discipline rule (per `CLAUDE.md`) requires error-code claims to ship with same-commit spec entries and `TestDocClaim_*` registrations.

---

## Phase 3 — Read path and sync

### Ticket 5: read-path TTL cache with bypass overrides

The read-side counterpart to ticket 3's write path.

**Scope.**

- `act show`/`ready`/`log`/`list`/`search` check `.act/.git/FETCH_HEAD` mtime. If within `act.readCacheTTLSeconds`, read state directly. If stale, `git fetch && git rebase origin/<branch>` (using ticket 2's helper for the rebase failure-handling path), then read.
- `ACT_DISPATCH_MODE=1` env var: bypass cache, always fetch.
- `--fresh` / `--no-cache` flag: bypass cache, always fetch.
- Post-rebase invariant: if rebase added commits, invalidate `.act/fold-checkpoint.json` and `.act/index.db` (delete or mark stale).

**Acceptance criteria.**

- `act ready` within 5s of a prior `act ready` does not fetch (FETCH_HEAD mtime unchanged); after 5s, it does (FETCH_HEAD mtime advances).
- `ACT_DISPATCH_MODE=1 act ready` always advances FETCH_HEAD mtime.
- After a successful rebase that adds new ops, `.act/fold-checkpoint.json` does not survive — verified by reading it before and after.

**Test plan.** Integration tests using two clones of a fixture remote: simulate a peer push, then test cache behavior on the other side. Spec/test-discipline entry for the TTL behavior.

**Files touched.** `internal/cli/show.go`, `internal/cli/ready.go`, `internal/cli/log.go`, `internal/cli/list.go`, `internal/cli/search.go` (read paths), `internal/cli/cache.go` (new, shared cache logic), `internal/fold/checkpoint.go` (invalidation hook).

### Ticket 6: `act remote sync` subcommand + trigger wiring

The upstream replication subcommand and the two trigger points.

**Scope.**

- `act remote sync`: pushes the orchestrator's `.act/.git` to `origin-upstream` if configured. Idempotent (no-op if `origin-upstream` is at the same ref as `origin`). Logs to `.act/.sync-log` on failure (append-only, capped). Returns zero exit on both success and silently-handled failure (fail-soft); non-zero only on configuration errors (no upstream configured).
- Post-receive hook content for `.act/.git/hooks/post-receive`: detaches and runs `nohup act remote sync &` in the background. Installed (filled in) by ticket 1's `act remote enable`.
- `actGitOps.Commit()` on the orchestrator: after successful commit on a remote-configured project, fires `act remote sync` in the background. Detection-of-orchestrator-vs-worker: if `origin` is `.` or a path under the same filesystem root as `.act/`, this is the orchestrator. Otherwise it's a worker (origin points elsewhere).

**Acceptance criteria.**

- `act remote sync` with `origin-upstream` configured: pushes successfully; FETCH_HEAD on `origin-upstream` advances; `.act/.sync-log` unchanged.
- `act remote sync` with `origin-upstream` unreachable: returns exit 0; `.act/.sync-log` has a new entry.
- A worker push that lands on the orchestrator triggers the post-receive hook, which runs `act remote sync` in the background — verified by tailing the sync-log and watching for entries after a controlled worker push.
- Orchestrator's own `act create` on a remote-configured project produces both a local commit and a background sync invocation — verified the same way.

**Test plan.** Two-clone integration test with a third "upstream" bare repo. Async timing tested with bounded waits and explicit notification (touch a file, then poll).

**Files touched.** `internal/cli/remote_sync.go` (new), `internal/gitops/gitops.go` (orchestrator-detection + background invocation), `internal/cli/remote.go` (post-receive hook installation in ticket 1's `act remote enable` is finalized here).

---

## Phase 4 — Bootstrap, harvest narrowing, doctor, integration

### Ticket 7: `act bootstrap-worker --from-remote`

The replacement for Phase 1.5's copy-on-dispatch for remote-attached workers.

**Scope.**

- `act bootstrap-worker --from-remote <url> <worktree-path>`: clones with `--depth 1`, into `<worktree-path>/.act.bootstrap/` (temp), then atomic-renames to `<worktree-path>/.act/`. Honors `act.bootstrapTimeoutSeconds`. On success, runs `act ready` in the new clone to validate.
- Preserves the existing `act bootstrap-worker --import-from <path>` mode for sandboxed-no-network workers (Phase 1.5 path stays).
- Rejects bootstrapping into a non-empty target unless `--force`.

**Acceptance criteria.**

- Bootstrap from a local fixture remote succeeds; round-trip `act ready` returns the same state as the source.
- Bootstrap with a stalled clone (test fixture: server pauses mid-transfer) hits the timeout, cleans up the temp dir, returns `bootstrap_timeout` error.
- Concurrent bootstraps to different target paths succeed without interfering.
- Bootstrap into a non-empty target errors with `target_not_empty`; `--force` allows overwrite.

**Test plan.** Local bare repo as fixture remote. Slow-network simulation for timeout test (custom `git daemon` with sleep). Spec entries for the new error codes.

**Files touched.** `cmd/act/bootstrap_worker.go` (new), `internal/cli/bootstrap_worker.go` (extend existing import-from path).

### Ticket 8: harvest narrowing + idempotency test enforcement

Phase 1.5's `act harvest` shrinks to the fallback role.

**Scope.**

- `act harvest <worker-path>`: same surface as Phase 1.5. New behavior: if `<worker-path>/.act/.git` has `origin` configured matching the current orchestrator's `.act/.git`, the worker was a remote-attached Phase 2 worker — log "harvest skipped, worker was push-attached" and exit 0 with empty output. If origin is unset or doesn't match, run the Phase 1.5 path (file-diff and copy).
- Idempotency test: same as the existing Phase 1.5 test plus a new test that runs harvest twice against a remote-attached worker — both no-op.
- Update `/orchestrate.md` to clarify when harvest still runs (sandboxed, crash recovery) vs. when it's skipped (normal remote-attached worker teardown).

**Acceptance criteria.**

- Harvest of a remote-attached worker that pushed during execution: zero new ops on the orchestrator post-harvest, exit 0, "skipped" log line.
- Harvest of a sandboxed worker (no `origin` config): existing Phase 1.5 behavior.
- Harvest of a worker that local-committed but never pushed (crashed mid-execution): copies the local commits' ops, exit 0.

**Test plan.** Direct extension of the Phase 1.5 round-trip test ticket (already filed as part of the in-flight tickets). Add three new test cases as named above.

**Files touched.** `internal/cli/harvest.go` (extend existing — Phase 1.5 harvest implementation lands first per dependency).

### Ticket 9: doctor extensions — three-state reconciliation + slow-writes + upstream-drift

The doctor side of Phase 2 visibility.

**Scope.**

- Add cases (a'), (c'), (f), (g), (h) detection per the brief's doctor table.
- New `--no-fetch` flag to skip the inline fetch (preserves Phase 1's offline doctor behavior).
- Remote-status block in JSON output: `remote_reachable`, `local_unpushed_count` (case f), `upstream_drift_commits` (case h), `slow_writes_last_hour`, `fetch_failure_reason` (case g).
- Exit-code mapping: case (g) → exit 4 unless `--no-fetch`; cases (f) and (h) → warn (exit 0 with stderr findings unless `--strict`).

**Acceptance criteria.**

- Doctor on a project with two unpushed local commits flags case (f) and lists the issue ids.
- Doctor with `origin` unreachable flags case (g) and exits 4 (or warn under `--no-fetch`).
- Doctor with `origin-upstream` 60 commits behind `origin` flags case (h) and suggests `act remote sync`.
- Doctor's `.act/.slow-writes`-based summary correctly counts entries within the last hour.

**Test plan.** Extends the Phase 1 doctor test suite. Each new case gets its own `TestDocClaim_*` registry entry.

**Files touched.** `internal/cli/doctor.go`, `internal/cli/doctor_test.go`, `docs/spec-v2.md` (doctor case extensions).

### Ticket 10: `/orchestrate` doc updates + workflow integration

The workflow doc captures the new lifecycle.

**Scope.**

- Update `~/.claude/commands/orchestrate.md` to use `act bootstrap-worker --from-remote` instead of copy-based bootstrap for remote-attached dispatches.
- Document the cutover: `act remote enable` is the one-time per-project step; after that, `/orchestrate` is unchanged from the operator's perspective.
- Document the failure modes (remote unreachable, slow filesystem, push exhaustion) with the operator-side recovery actions.
- Add a "Phase 1.5 → Phase 2 cutover" section in the docs explaining when to run `act remote enable` and what changes.

**Acceptance criteria.**

- Doc updates reference all new subcommands by exact name.
- `TestDocClaim_*` registry entries for: the orchestrate-doc dispatch step, the cutover instruction, the failure-mode documentation.

**Test plan.** Doc-claim sweep. No runtime test.

**Files touched.** `~/.claude/commands/orchestrate.md` (live edit through symlink), `internal/skill/SKILL.md` (worker-protocol section update), `docs/migration-runbook.md` (Phase 2 section).

### Ticket 11: end-to-end integration test suite

The integration cover.

**Scope.** Comprehensive E2E tests, one per major behavior:

- Two-machine round-trip (simulated by two `.act/.git` clones on a single test host, both pointing at a third bare-repo fixture as their `origin`).
- Push contention under high concurrency (4 parallel workers, 50 ops each, all succeed without loss).
- Upstream drift: orchestrator writes 60 ops without sync; doctor flags case (h); `act remote sync` clears.
- Slow filesystem: 5 ops with 2-second injected commit delay; `.act/.slow-writes` accumulates; doctor surfaces the summary.
- Dispatch loop: simulate `/orchestrate` issuing two parallel bootstrap-workers, each running an `act create` + `act close` cycle; orchestrator's post-receive triggers fire; upstream sync log shows both events.

**Acceptance criteria.** Each behavior has at least one passing E2E test. CI runtime budget: ~2 minutes total (each test sub-minute).

**Test plan.** Lives in `internal/integration/phase2_test.go` (new package). Uses `t.Parallel` aggressively.

**Files touched.** New test package only.

---

## Dependency graph

```
                    Ticket 1 (foundation)
                   /          |          \
              Ticket 2    Ticket 5    Ticket 7
              (push-retry)  (read cache) (bootstrap-worker)
                  |             |              |
              Ticket 3      Ticket 6           |
              (push-on-write)  (remote sync)   |
                  \          /                /
                   \        /                /
                    Ticket 8 (harvest narrow)
                            |
                    Ticket 9 (doctor)
                            |
                    Ticket 10 (orchestrate doc)
                            |
                    Ticket 11 (E2E integration)
```

Sequential bottlenecks: 1 → 2 → 3 forms the critical path through the write side. Ticket 5 (read cache) and ticket 7 (bootstrap-worker) are independent after 1 and run in parallel with 2/3. Ticket 6 (sync) needs 1 and 3 (write-path trigger).

Parallelization opportunities under `/orchestrate`:

- After 1 lands: 2, 5, 7 in parallel (three workers).
- After 2 lands: 3 (single worker — write paths touch many files, high merge risk).
- After 3 lands: 6 (single worker — depends on write path).
- After 5 lands: 8 can start (parallel with 6).
- After 6 and 8 land: 9 (single worker, doctor changes are localized).
- After 9 lands: 10 (doc only) and 11 (E2E tests) in parallel.

Worst case wall-clock with serial dispatch: ~11 dispatch cycles. With aggressive parallelism: ~7 cycles. At ~1-2 cycles/day, this is 5-10 working days of orchestrate time.

## Open implementation questions

Plan-stage details that don't gate the plan but need to be settled before the relevant ticket starts:

1. **Slow-write log format.** JSON-lines with `{timestamp, op_id, duration_ms, op_type}` is a reasonable default but worth confirming the schema before ticket 3 starts.
2. **`.act/.sync-log` retention policy.** Capped at how many entries? Cleared on what trigger? Probably last-100-or-last-7-days, whichever shorter — needs to be set in ticket 6.
3. **Worker telemetry coupling.** The brief flags this as a remaining open question. For this plan, scope it out — leave as a follow-up after Phase 2 ships.
4. **Detection of "this is the orchestrator" in `actGitOps`.** The plan proposes "origin is `.` or a path under the same filesystem root." This is a heuristic; needs a clean check in ticket 6. Alternative: store an explicit `act.role=orchestrator` config key set by `act remote enable`. The config-key approach is more robust; consider switching to it in ticket 6 implementation.

## Risks and rough-edges

- **Concurrent write path edits.** Ticket 3 touches every write subcommand. Cannot be parallelized with any other write-path ticket. This is the largest serialization point.
- **`updateInstead` cross-platform behavior.** Validated on macOS (Andrew's machine). Linux should be identical; Windows is untested (and Phase 1 has open Windows tickets — `act-2f3d` for op filename colons). The plan does not address Windows; it inherits Phase 1's status.
- **Test-fixture remotes.** Using `git daemon` for integration test remotes works but adds process management. Alternative: SSH-loopback. Picking one early avoids reinventing per ticket.
- **Phase 1.5 dependency.** Phase 2 builds on Phase 1.5's `act bootstrap-worker --import-from` and `act harvest` subcommands. Phase 1.5 is currently in-flight (umbrella `act-b77a80`); Phase 2 implementation should not start until Phase 1.5 lands. Verify before kicking off ticket 1.

## Cross-references

- Design brief v4: `docs/coordination-plane-phase2-design.md` (commit `a7f1bd1`).
- Phase 1 design: `docs/coordination-plane-design.md` v2.1.
- Phase 1.5 umbrella: `act-b77a80` (current in-flight work).
- Project conventions: `CLAUDE.md` documentation discipline section; commit-marker trailer rule.
