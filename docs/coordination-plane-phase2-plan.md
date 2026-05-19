# Coordination plane — Phase 2 implementation plan

**Status:** v2 draft, awaiting second plan-review gate.
**Author:** drafted in session 2026-05-18, following the design brief at `docs/coordination-plane-phase2-design.md` v4 (commit `a7f1bd1`).
**Scope:** turn the design brief into a sequenced, scoped implementation. Thirteen tickets across four phases of integration. Each ticket has acceptance criteria, test plan, and explicit dependencies.

**Iteration history.** v1 → v2 changes incorporated 2026-05-18; see `docs/reviews/plan-v1-synthesis-2026-05-18.md`. Net structural changes from v1: ticket 4 dropped (was a doc-discipline reminder, not a real ticket); tickets 1, 3, and 6 each split into `a`/`b` halves to unlock parallelism and tighten file-overlap dep edges; dep graph gains four explicit edges (1a → 1b, 3a → 5, 3a → 6b, `act-b77a80` → 1a); open-questions section shrinks from four to one (worker telemetry, deferred to Phase 3); acceptance criteria for tickets 3a/3b/6a/6b/9/10 tightened to name user-visible surfaces; `internal/testfixtures/remote.go` ownership pinned to ticket 2; `gitops.FetchAndRebase` helper pinned in ticket 2's design and consumed by 3a/5; `act.role` config key pinned in ticket 1a's schema (closes OQ #4); slow-write log schema pinned in ticket 3b (closes OQ #1); shallow + repeated-contention + exhaustion test added to ticket 2.

## Plan structure

Four phases, sequenced by dependency. Within each phase, the named tickets can run in parallel under `/orchestrate` (provably disjoint files in most cases — collisions called out where they exist).

1. **Foundation** — config, hooks, the `act remote enable/disable` UX, doctor scaffolding for the new config keys (ticket 1a). The `act remote add-upstream` flow (ticket 1b) is foundational by topic but parallelizes with Phase 2 because only ticket 6 reads upstream config.
2. **Write path** — push-on-write integration (3a), retry helper (2), offline + slow-write logging (3b), error envelope additions. The largest phase by code volume.
3. **Read path and sync** — TTL cache (5), `ACT_DISPATCH_MODE` bypass, `act remote sync` subcommand + hook content (6a), orchestrator write-trigger (6b).
4. **Bootstrap, harvest narrowing, doctor extensions, integration** — `act bootstrap-worker --from-remote` (7), harvest narrowed to fallback role (8), doctor's three-state reconciliation cases (9), `/orchestrate` workflow updates (10), end-to-end integration tests (11).

Total: ~3500-5000 lines of code + tests across the thirteen tickets. Roughly 2-3 weeks of `/orchestrate` time at the dogfood pace we ran last week.

**Cross-cutting constraint (replaces v1 ticket 4).** Every ticket that introduces a new error code MUST in the same commit (a) add the code to `docs/spec-v2.md`'s error-table, (b) register a `TestDocClaim_*` entry in `internal/cli/docs_sweep_test.go`, and (c) emit the code through the standard envelope shape (`Envelope.Code`, `Envelope.Details`, exit code per the universal table). This is restated in each ticket's "Files touched" line where applicable; v1's ticket 4 has been dropped because the discipline rule is already enforced by `CLAUDE.md` and the sweep test — a dedicated ticket added a no-op slot to the count.

---

## Phase 1 — Foundation

### Ticket 1a: `act remote enable / disable` + config keys + hook skeleton

The clean foundation. Everything downstream depends on this.

**Blocked-by.** `act-b77a80` (Phase 1.5 umbrella). Hard edge — see "Dependency graph" below. An orchestrator dispatching 1a cold without Phase 1.5 landed will fail because ticket 7 (bootstrap-worker `--from-remote`) extends the existing `--import-from` path that Phase 1.5 ships.

**Scope.** New subcommands and the config schema they manipulate.

- `act remote enable`:
  - Sets `receive.denyCurrentBranch=updateInstead` on `.act/.git`.
  - Sets config keys: `act.readCacheTTLSeconds=5`, `act.bootstrapTimeoutSeconds=30`, `act.fetchTimeoutSeconds=10`, `act.slowWriteThresholdMs=1000`, `act.upstreamDriftThresholdCommits=50`, `act.upstreamDriftThresholdSeconds=3600`, `act.role=orchestrator`.
  - Installs the `.act/.git/hooks/post-receive` skeleton (no-op body; the script-content is filled in by ticket 6a).
  - Runs `act doctor` post-config to verify.
- `act remote disable`:
  - Unsets all the keys above (including `act.role`).
  - Removes the post-receive hook file at `.act/.git/hooks/post-receive` (not just unsetting config — the file itself).
  - Idempotent: running twice is a no-op the second time; running on a never-enabled repo is a no-op.

**Decision pinned: `act.role` config key (closes OQ #4 from v1).** `act remote enable` sets `act.role=orchestrator`. Workers bootstrapped via `act bootstrap-worker --from-remote` get `act.role=worker` written by the bootstrap step (see ticket 7). Ticket 6b reads this key to decide whether to fire the upstream-sync trigger after a local commit. No filesystem-path heuristic — config-key is the only mechanism. If the key is unset (legacy or hand-crafted repo), the default is `worker` (i.e., do not trigger upstream sync) — safe-by-default.

**Acceptance criteria.**

- `act remote enable && git config -f .act/.git/config receive.denyCurrentBranch` outputs `updateInstead`.
- `act remote enable && git config -f .act/.git/config act.role` outputs `orchestrator`.
- `act remote enable && test -f .act/.git/hooks/post-receive` succeeds (hook skeleton file present).
- `act remote disable && git config -f .act/.git/config --get receive.denyCurrentBranch` returns non-zero (unset).
- `act remote disable && git config -f .act/.git/config --get act.role` returns non-zero (unset).
- `act remote disable && test -f .act/.git/hooks/post-receive` returns non-zero (file removed).
- `act remote disable` run twice in a row exits zero both times (idempotent).
- `act doctor` post-enable returns zero findings.

**Test plan.** Subcommand-level integration tests using temp dirs and a fixture `.act/.git`. Each acceptance row has a corresponding `TestDocClaim_*` entry in `internal/cli/docclaim_test.go` and a registry tuple in `internal/cli/docs_sweep_test.go`. Hook-file-removal test asserts on filesystem state (`os.Stat` returning `ENOENT`), not on `git config`.

**Files touched.** `cmd/act/remote.go` (new — `enable` and `disable` subcommands only), `cmd/act/main.go` (dispatcher entry), `internal/cli/remote.go` (new), `internal/config/config.go` (new config keys including `act.role`). `docs/spec-v2.md` (config-key additions, `act.role` semantics). No new error codes introduced by 1a.

***Changes from v1:*** *Renamed from "Ticket 1" to "Ticket 1a"; `add-upstream` carved out to ticket 1b. `act.role` config key added and named here (closes synthesis C1 / OQ #4). `act remote disable` acceptance tightened to assert hook-file removal at filesystem level (synthesis S4). `act-b77a80` blocked-by relationship made an explicit hard edge (synthesis S6).*

### Ticket 1b: `act remote add-upstream` + `--force-public` + `upstream_public` error

The upstream-replication wiring. Only ticket 6a consumes it; can parallelize with most of Phase 2.

**Blocked-by.** Ticket 1a (shares the same `internal/cli/remote.go` package, and the `add-upstream` subcommand registers in the same dispatcher slot).

**Scope.**

- `act remote add-upstream <url>`: adds an `origin-upstream` remote pointing at `<url>`; does an initial `git push origin-upstream <branch>`; refuses public-URL upstreams.
- Public-URL detection: matches against a curated list of public host patterns (`github.com/aac/public-*`, `*.public.example.com`, etc. — concrete list in `internal/config/upstream_patterns.go`). On public-pattern match: returns `upstream_public` error (envelope code `upstream_public`, exit 2) unless `--force-public` is passed.

**Acceptance criteria.**

- `act remote add-upstream https://github.com/aac/private-thing` succeeds for a private (non-pattern-matching) repo; the initial push lands on the bare-repo fixture; `git config -f .act/.git/config remote.origin-upstream.url` returns the URL.
- `act remote add-upstream https://github.com/aac/public-thing` errors with envelope `{code: "upstream_public", exit: 2}` and a stderr message containing the literal string `refusing public upstream; pass --force-public to override`.
- `act remote add-upstream https://github.com/aac/public-thing --force-public` succeeds and the initial push lands.

**Test plan.** Integration test using a bare-repo fixture from `internal/testfixtures/remote.go` (owned by ticket 2; see C6). Public-URL detection tested with table-driven cases. `TestDocClaim_*` entry for the literal `upstream_public` stderr string.

**Files touched.** `cmd/act/remote.go` (extend with `add-upstream` subcommand), `internal/cli/remote_upstream.go` (new), `internal/cli/errors.go` (add `upstream_public` constant), `internal/config/upstream_patterns.go` (new), `docs/spec-v2.md` (error-table entry for `upstream_public`), `internal/cli/docs_sweep_test.go` (registry entry).

***Changes from v1:*** *New ticket — split out of v1 ticket 1 so it can parallelize with Phase 2 work instead of gating it (synthesis S2). Acceptance criteria tightened to name the literal stderr string (synthesis C4 audit pass).*

---

## Phase 2 — Write path

These tickets are tightly coupled. Ticket 2 produces helpers (`PushWithRetry` and `FetchAndRebase`) that tickets 3a and 5 both consume. Tickets 3a and 5 both edit `internal/gitops/gitops.go` — they cannot run in parallel; 5 rebases on 3a.

### Ticket 2: push-contention retry helper + fixture-remote owner + `gitops.FetchAndRebase` extraction

The reusable git-level helpers. No CLI surface.

**Scope.**

1. `gitops.PushWithRetry(branch string, opts PushOpts) error` — implements the v4 brief's "fetch, rebase, push, verify" loop:
   - `git push origin <branch>`.
   - On non-fast-forward: invoke `gitops.FetchAndRebase` (see helper extraction below); goto push.
   - On apparent success: `git fetch origin <branch>`; `git merge-base --is-ancestor <local-HEAD> origin/<branch>`. If non-ancestor, treat as silent rejection; goto fetch-and-rebase.
   - On rebase failure with `shallow file has changed` or `unable to find common ancestor`: `git fetch --unshallow origin <branch>` once (then continue retrying within the cap).
   - After N (default 5) full retries with exponential backoff capped at 1s, return `ErrPushExhausted`.
2. `gitops.FetchAndRebase(branch string) error` — extracted helper. Both 3a (`PushWithRetry`'s inner loop) and ticket 5 (read-path cache miss) call it. Encapsulates `git fetch origin <branch>` + `git rebase origin/<branch>` + shallow-fallback detection. Returns typed errors for the caller's retry-or-bail decision: `ErrRebaseConflict`, `ErrShallowExhausted`, `ErrFetchFailed`.
3. `internal/testfixtures/remote.go` — shared package owning fixture-remote infrastructure. Tickets 1b/6a/6b/7/11 import. **Picked shape: bare-repo-on-filesystem.** A bare repo at a tempdir path, accessed via the local-filesystem URL scheme (no `git daemon`, no SSH-loopback). Simplest, no process management, deterministic timing. Public API: `NewBareRemote(t *testing.T) *BareRemote` returning `{URL, Path, Cleanup}`; helpers for advancing the remote (`AdvanceCommits(n int)`), simulating contention (`ConcurrentPush(...)`), and shallow setups (`InitShallow(depth int)`).

**Pinned helper shape (closes synthesis S7).** `gitops.FetchAndRebase` is the named extraction point. Both 3a (writer) and 5 (reader) consume it; neither re-implements rebase recovery. This is a v1 → v2 factoring decision pinned here in the helper-owner ticket so 3a and 5 can be written in any order against a stable contract.

**Acceptance criteria.**

- Two parallel processes pushing to the same bare repo (via the fixture): both eventually succeed, neither loses commits. Asserted by post-test inspection of `.act/ops/` showing the union of both writers' op files.
- Simulated `updateInstead` dirty-tree silent rejection (fixture dirties the receiving working tree mid-push): caller's retry loop detects it via reachability check and recovers within the N-retry cap.
- Simulated shallow-clone rebase failure (fixture creates a `--depth 1` clone and advances the remote beyond it): `--unshallow` fallback triggers; subsequent retries succeed.
- **New test case (synthesis S5):** shallow + repeated-contention + exhaustion. Fixture creates a shallow clone; concurrent writer advances the remote between every push attempt the helper makes. The helper exhausts its retry cap before the single `--unshallow` round can recover. Result: returns `ErrPushExhausted` with `details.shallow_unshallow_attempted=true`, `details.retry_count=5`. Verifies the cap holds even when shallow recovery is in the loop.
- Exhausted retries return `ErrPushExhausted` with `Envelope.Code = push_exhausted` and `details.retry_count`.
- `gitops.FetchAndRebase` called against a remote with no diverging history is a no-op (clean exit, no rebase invoked).
- `internal/testfixtures/remote.go` exposes a documented public API used by at least one test in this ticket and ready for import by 1b/6a/6b/7/11.

**Test plan.** Concurrency tests using `t.Parallel` with two goroutines invoking the helper against a shared `BareRemote` fixture. Shallow-clone failure simulated by `BareRemote.InitShallow()`. Spec entries for `push_exhausted` and `remote_unreachable` codes go in the same commit. Backoff base value is the ticket implementer's call (out of scope per synthesis).

**Files touched.** `internal/gitops/push_retry.go` (new), `internal/gitops/fetch_rebase.go` (new — `FetchAndRebase` helper), `internal/gitops/push_retry_test.go` (new), `internal/gitops/fetch_rebase_test.go` (new), `internal/testfixtures/remote.go` (new — shared fixture package), `internal/testfixtures/remote_test.go` (new), `internal/cli/errors.go` (two new constants: `push_exhausted`, `remote_unreachable`), `docs/spec-v2.md` (error-table entries), `internal/cli/docs_sweep_test.go` (two `TestDocClaim_*` registry entries).

***Changes from v1:*** *Now owns `internal/testfixtures/remote.go` (synthesis C6) with a pinned bare-repo-on-filesystem shape. Now owns the `gitops.FetchAndRebase` extraction (synthesis S7). New shallow-plus-repeated-contention exhaustion test case (synthesis S5). The four-edge dep graph addition runs through this ticket — it's now the convergence point for several downstream consumers.*

### Ticket 3a: push-on-write integration

Wire ticket 2's `PushWithRetry` helper into every `act` write path. No new flags, no logging.

**Blocked-by.** Ticket 2 (consumes `PushWithRetry` and the shared `FetchAndRebase`).

**Scope.**

- After every `actGitOps.Commit()` on a remote-configured project (origin is set), invoke `gitops.PushWithRetry`.
- On `ErrPushExhausted`, return the envelope to the caller. Write-side ops do not roll back the local commit — the op file exists locally and recovers via harvest if needed.
- Updates the actGitOps integration in `internal/gitops/gitops.go` so every write subcommand (`create`, `close`, `update`, `dep-add`, `reopen`, `delete`) inherits the push automatically.

**Acceptance criteria.**

- `act create` on a remote-configured project pushes synchronously by default. `act log` on a peer worker's clone shows the new op after `act create` returns.
- `act close` on a remote-configured project pushes; the close-op file is visible on the peer clone after the command returns.
- After 5 push retry exhaustion (fault-injected via the test-only `ACT_TEST_FAIL_PUSH_AFTER=N` env hook documented in this ticket), `act close` returns envelope `{code: "push_exhausted", exit: 4}` with `details.retry_count=5`.
- All six write subcommands invoke `PushWithRetry` exactly once per successful commit — asserted via a counting hook in the helper.

**Test plan.** Integration tests using `internal/testfixtures/remote.go`'s `BareRemote` and `ConcurrentPush` helpers. Each write subcommand gets a smoke test asserting the push happens. Spec/test-discipline entries for the `push_exhausted` envelope surface (already shipped by ticket 2; this ticket adds the call-site assertions).

**Files touched.** `internal/gitops/gitops.go` (actGitOps integration — the file ticket 5 also edits; 5 rebases on this), `internal/cli/create.go`, `internal/cli/close.go`, `internal/cli/update.go`, `internal/cli/depadd.go`, `internal/cli/reopen.go`, `internal/cli/delete.go` (all six write paths). Bundle dispatcher conflict risk: high — all write paths touched. **Do not run in parallel with any other write-path-touching ticket, and ticket 5 must rebase on this ticket.**

***Changes from v1:*** *Split out of v1 ticket 3 (synthesis C3). Just the push-integration concern — `--offline` and slow-write logging moved to ticket 3b. Explicit dep edge added to ticket 5 because both edit `internal/gitops/gitops.go`.*

### Ticket 3b: `--offline` flag + slow-write logging

The two write-path features that don't share file overlap with the read-path.

**Blocked-by.** Ticket 3a (extends the same actGitOps commit code path 3a establishes).

**Scope.**

- `--offline` flag on all write subcommands: commits locally, skips the push, records "pending push" in a state file (`.act/.pending-pushes`) that the next non-offline write retries before its own push.
- Slow-write measurement: `actGitOps.Commit()` measures monotonic time between stage and commit. If duration > `act.slowWriteThresholdMs`, emit a warning to stderr and append a structured JSON line to `.act/.slow-writes` (rolling, capped at 100 entries — older entries pruned in-place).
- Fault-injection hook: `ACT_TEST_SLOW_COMMIT_MS=<n>` env var, gated to test builds (build-tag `acttest`), injects a `time.Sleep` in the commit path so tests can drive the warning deterministically. Documented in `internal/gitops/gitops.go` next to the hook.

**Pinned slow-write log schema (closes OQ #1 from v1).** `.act/.slow-writes` is JSON-lines (one record per line, newline-terminated). Each record is exactly:

```json
{"timestamp": "2026-05-18T14:23:01.234Z", "op_id": "act-abc123", "duration_ms": 1247, "op_type": "create"}
```

Fields:
- `timestamp` — RFC3339 with millisecond precision, UTC (`Z`).
- `op_id` — full id of the op being committed (the op the slow commit is for).
- `duration_ms` — integer milliseconds, monotonic delta between stage and commit.
- `op_type` — one of `create`, `close`, `update`, `dep_add`, `reopen`, `delete` (matches the op-type enum already in the op envelope).

The file is read by ticket 9's doctor case (g) summary — same schema, no transform.

**Pinned stderr slow-write warning text (synthesis C4).** The warning emitted to stderr is:

```
act: slow write detected (1247ms > 1000ms threshold); see .act/.slow-writes
```

Literal pattern: `act: slow write detected (<n>ms > <threshold>ms threshold); see .act/.slow-writes`. The `TestDocClaim_*` greps for the literal prefix `act: slow write detected (` and the literal suffix `; see .act/.slow-writes`.

**Acceptance criteria.**

- `act create --offline` commits locally without pushing; `.act/.pending-pushes` gains a record with the local commit's SHA.
- A subsequent `act create` (non-offline) flushes the pending push before its own — verified by asserting the pending-pushes file is empty after the second command, and both commits land on the remote.
- Fault-injected slow commit (`ACT_TEST_SLOW_COMMIT_MS=2000`) produces a stderr line matching the literal slow-write warning pattern above.
- The same fault-injected commit produces exactly one new JSON-line record in `.act/.slow-writes` matching the pinned schema. Test asserts all four field names present, `duration_ms >= 2000`, `op_type` matches the command, `op_id` matches the created op's id.
- `.act/.slow-writes` is capped at 100 entries: after 105 slow writes (driven via the fault-injection hook), the file contains exactly 100 records and the first five are absent (oldest pruned).

**Test plan.** Integration tests with the fault-injection env hook driving the slow path. `TestDocClaim_*` entries: one for the stderr warning literal, one for the `.act/.slow-writes` schema (asserting field names, not values), one for the cap-at-100 pruning.

**Files touched.** `internal/gitops/gitops.go` (slow-write measurement and pending-push integration — rebases on ticket 3a's changes to the same file), `internal/cli/offline.go` (new, shared `--offline` flag handling), `internal/cli/slowwrites.go` (new — log writer with cap-prune logic). All six write-path command files re-touched only to wire the `--offline` flag declaration; the call-site changes from 3a are not re-touched. `docs/spec-v2.md` (offline-flag documentation, slow-write warning behavior, `.act/.slow-writes` schema). `internal/cli/docs_sweep_test.go` (three registry entries: warning text, schema, pruning).

***Changes from v1:*** *Split out of v1 ticket 3 (synthesis C3). Slow-write log schema pinned here (closes OQ #1; synthesis C2). Stderr slow-write warning text pinned to a literal pattern (synthesis C4 audit). Fault-injection env hook named (synthesis C4 — was "fault-injection hook not designed" in the architect review).*

---

## Phase 3 — Read path and sync

### Ticket 5: read-path TTL cache with bypass overrides

The read-side counterpart to ticket 3a's write path. Consumes `gitops.FetchAndRebase` from ticket 2.

**Blocked-by.** Ticket 3a (file overlap — both edit `internal/gitops/gitops.go`; ticket 5 rebases on 3a) AND ticket 2 (consumes `FetchAndRebase`). After 3a lands and 5 rebases, the two changes coexist cleanly because 3a edits the commit path and 5 edits the read path within the same file.

**Scope.**

- `act show`/`ready`/`log`/`list`/`search` check `.act/.git/FETCH_HEAD` mtime. If within `act.readCacheTTLSeconds`, read state directly. If stale, call `gitops.FetchAndRebase(branch)`, then read.
- `ACT_DISPATCH_MODE=1` env var: bypass cache, always fetch.
- `--fresh` / `--no-cache` flag: bypass cache, always fetch.
- Post-rebase invariant: if rebase added commits, invalidate `.act/fold-checkpoint.json` and `.act/index.db` (delete or mark stale).

**Acceptance criteria.**

- `act ready` within 5s of a prior `act ready` does not fetch (FETCH_HEAD mtime unchanged); after 5s, it does (FETCH_HEAD mtime advances).
- `ACT_DISPATCH_MODE=1 act ready` always advances FETCH_HEAD mtime, regardless of the prior call's recency.
- `act ready --fresh` always advances FETCH_HEAD mtime; `act ready --no-cache` is an alias with the same behavior.
- After a successful rebase that adds new ops, `.act/fold-checkpoint.json` does not survive — verified by reading it before and after the cache-miss path.

**Test plan.** Integration tests using two clones of a `BareRemote` fixture (from `internal/testfixtures/remote.go`): simulate a peer push, then test cache behavior on the other side. Spec/test-discipline entry for the TTL behavior and the bypass flags.

**Files touched.** `internal/cli/show.go`, `internal/cli/ready.go`, `internal/cli/log.go`, `internal/cli/list.go`, `internal/cli/search.go` (read paths), `internal/cli/cache.go` (new, shared cache logic), `internal/fold/checkpoint.go` (invalidation hook), `internal/gitops/gitops.go` (the read-path side; rebases on 3a's changes to the same file).

***Changes from v1:*** *Explicit dep edge added to ticket 3a (synthesis C3 — they both edit `internal/gitops/gitops.go`; ticket 5 rebases on 3a). Consumes `gitops.FetchAndRebase` (synthesis S7) instead of inlining rebase logic.*

### Ticket 6a: `act remote sync` subcommand + post-receive hook content

The subcommand surface and the hook script body. No orchestrator-write trigger yet — that's ticket 6b.

**Blocked-by.** Ticket 1a (consumes the post-receive skeleton 1a installs).

**Scope.**

- `act remote sync`: pushes the orchestrator's `.act/.git` to `origin-upstream` if configured. Idempotent (no-op if `origin-upstream` is at the same ref as `origin`). Logs to `.act/.sync-log` on failure (append-only, capped at 100 entries — same pruning shape as `.slow-writes`). Returns zero exit on both success and silently-handled failure (fail-soft); non-zero only on configuration errors (no upstream configured).
- Post-receive hook content for `.act/.git/hooks/post-receive`: detaches and runs `nohup act remote sync &` in the background. The hook-content fill-in lives here (ticket 1a installs the empty file; this ticket gives it a body — `act remote enable` will pick up the body via a templated install once 6a lands and 1a is re-touched to read the template).

**Acceptance criteria.**

- `act remote sync` with `origin-upstream` configured and reachable: pushes successfully; remote ref on `origin-upstream` advances to match `origin`; `.act/.sync-log` unchanged (success is silent).
- `act remote sync` with `origin-upstream` unreachable (fixture: bare repo URL points at nonexistent path): exits zero; `.act/.sync-log` has a new entry whose first JSON field is `"reason": "unreachable"`.
- `act remote sync` with no `origin-upstream` configured: exits 2 with envelope `{code: "upstream_not_configured"}`, stderr contains literal string `no origin-upstream configured; run 'act remote add-upstream <url>'`.
- A worker push that lands on the orchestrator triggers the post-receive hook, which runs `act remote sync` in the background — verified by tailing `.act/.sync-log` and watching for entries after a controlled worker push from the fixture. **Verification mechanism (synthesis C4 W1):** the test asserts the sync-log file's mtime advances within a 2-second bounded wait after the worker push completes. Not a polling sleep — the test uses `notify`-style filesystem watch with timeout.

**Test plan.** Two-clone integration test using `BareRemote` plus a third "upstream" `BareRemote` instance. Async timing tested with bounded filesystem-watch waits, not arbitrary sleeps. `TestDocClaim_*` for the `upstream_not_configured` stderr literal and for the sync-log schema.

**Files touched.** `internal/cli/remote_sync.go` (new), `internal/cli/remote.go` (hook-content template added; 1a's `act remote enable` re-reads the template — this is the file 1a touched), `internal/cli/errors.go` (`upstream_not_configured` constant), `docs/spec-v2.md` (subcommand spec + sync-log schema + error-code entry), `internal/cli/docs_sweep_test.go` (registry entries).

***Changes from v1:*** *Split out of v1 ticket 6 (synthesis S3). The subcommand-and-hook half — no orchestrator-write trigger. Verification mechanism for the worker-push-triggers-hook test pinned to filesystem-watch with bounded timeout instead of v1's "verified the same way" punt (synthesis C4 W1). Acceptance for missing upstream tightened to a literal stderr string.*

### Ticket 6b: orchestrator-write upstream-sync trigger

The `actGitOps.Commit()` integration that fires `act remote sync` after a local commit on the orchestrator.

**Blocked-by.** Ticket 3a (the `actGitOps.Commit()` integration point lives in `internal/gitops/gitops.go` which 3a established; 6b extends the post-commit path 3a wires in) AND ticket 6a (consumes the `act remote sync` subcommand).

**Scope.**

- `actGitOps.Commit()`: after a successful commit on a remote-configured project, check `act.role` (ticket 1a's config key). If `act.role=orchestrator`, fork-exec `act remote sync` in the background (detached, stderr to `.act/.sync-log` if it fails). If `act.role=worker` or unset, do nothing.
- Orchestrator detection: read `act.role` via the config layer. No path-based heuristic. (Closes OQ #4 — see ticket 1a's "Decision pinned" block.)

**Acceptance criteria.**

- Orchestrator's own `act create` on a remote-configured project (where `act.role=orchestrator`) produces both a local commit and a background `act remote sync` invocation — verified by tailing `.act/.sync-log` for entries after the create, with a 2-second bounded filesystem-watch.
- Worker's `act create` on a remote-configured project (where `act.role=worker`) produces a local commit and a `PushWithRetry` push but does NOT invoke `act remote sync` — verified by asserting `.act/.sync-log` mtime is unchanged after the worker push.
- A repo with `act.role` unset (legacy state) is treated as worker — no upstream sync fired.

**Test plan.** Integration test using two `BareRemote` instances and `act remote enable` / `bootstrap-worker` to drive the role labels. Asserts on `.act/.sync-log` mtime as the trigger signal.

**Files touched.** `internal/gitops/gitops.go` (the post-commit hook in actGitOps — same file as 3a and 5; 6b rebases on 5 since 5 has already rebased on 3a by the time 6b runs). No new error codes. `docs/spec-v2.md` (orchestrator-write-trigger behavior section).

***Changes from v1:*** *Split out of v1 ticket 6 (synthesis S3). Orchestrator detection now reads `act.role` config (synthesis C1 / OQ #4 closure) instead of filesystem-path heuristic. Verification mechanism for the orchestrator-write trigger pinned to bounded filesystem-watch (synthesis C4).*

---

## Phase 4 — Bootstrap, harvest narrowing, doctor, integration

### Ticket 7: `act bootstrap-worker --from-remote`

The replacement for Phase 1.5's copy-on-dispatch for remote-attached workers.

**Blocked-by.** Ticket 1a (writes `act.role=worker` to the bootstrapped clone, which means the `act.role` config key must exist as a documented schema member before this ticket can rely on it).

**Scope.**

- `act bootstrap-worker --from-remote <url> <worktree-path>`: clones with `--depth 1` into `<worktree-path>/.act.bootstrap/` (temp), then atomic-renames to `<worktree-path>/.act/`. Honors `act.bootstrapTimeoutSeconds`. On success, sets `act.role=worker` in the new clone's config, then runs `act ready` to validate.
- Preserves the existing `act bootstrap-worker --import-from <path>` mode for sandboxed-no-network workers (Phase 1.5 path stays).
- Rejects bootstrapping into a non-empty target unless `--force`.

**Acceptance criteria.**

- Bootstrap from a local fixture remote (`BareRemote`) succeeds; round-trip `act ready` returns the same state as the source.
- After bootstrap, `git config -f <worktree-path>/.act/.git/config act.role` returns `worker`.
- Bootstrap with a stalled clone (test fixture: `BareRemote.PauseTransfer()` for synthesized hang) hits the timeout, cleans up the temp dir, returns envelope `{code: "bootstrap_timeout", exit: 4}` with `details.timeout_seconds` matching `act.bootstrapTimeoutSeconds`.
- Concurrent bootstraps to different target paths succeed without interfering.
- Bootstrap into a non-empty target errors with envelope `{code: "target_not_empty", exit: 2}`; `--force` allows overwrite.

**Test plan.** `BareRemote` fixture from `internal/testfixtures/remote.go`. Slow-network simulation via the fixture's pause-transfer helper (added in ticket 2's fixture package design). Spec entries for `bootstrap_timeout` and `target_not_empty`.

**Files touched.** `cmd/act/bootstrap_worker.go` (new), `internal/cli/bootstrap_worker.go` (extend existing import-from path), `internal/cli/errors.go` (`bootstrap_timeout`, `target_not_empty`), `docs/spec-v2.md` (subcommand spec + error-code entries), `internal/cli/docs_sweep_test.go` (registry entries).

***Changes from v1:*** *Writes `act.role=worker` post-clone (synthesis C1 follow-through). Consumes `BareRemote.PauseTransfer()` from ticket 2's fixture package (synthesis C6). Error-code envelope shapes named concretely.*

### Ticket 8: harvest narrowing + idempotency test enforcement

Phase 1.5's `act harvest` shrinks to the fallback role.

**Blocked-by.** Ticket 7 (the role-distinction logic — harvest needs to detect a remote-attached worker, which exists only after `act.role=worker` is written by ticket 7's bootstrap path).

**Scope.**

- `act harvest <worker-path>`: same surface as Phase 1.5. New behavior: if `<worker-path>/.act/.git`'s `act.role` is `worker` AND its `origin` matches the current orchestrator's `.act/.git` path, the worker was a remote-attached Phase 2 worker — log "harvest skipped, worker was push-attached" and exit 0 with empty output. Otherwise (no `act.role`, or `origin` doesn't match), run the Phase 1.5 file-diff-and-copy path.
- Update `~/.claude/commands/orchestrate.md` (handled by ticket 10) to clarify when harvest still runs vs. when it's skipped.

**Acceptance criteria.**

- Harvest of a remote-attached worker that pushed during execution (`act.role=worker`, `origin` matches): zero new ops on the orchestrator post-harvest, exit 0, stderr contains the literal string `harvest skipped, worker was push-attached`.
- Harvest of a sandboxed worker (no `act.role` config): existing Phase 1.5 behavior.
- Harvest of a worker that local-committed but never pushed (`act.role=worker` but local commits not yet on origin): copies the local commits' ops, exit 0.
- Harvest run twice against a remote-attached worker: both runs no-op, both exit 0. (Idempotency.)

**Test plan.** Direct extension of the Phase 1.5 round-trip test ticket. Add four test cases as named above. `TestDocClaim_*` for the literal "harvest skipped" stderr string.

**Files touched.** `internal/cli/harvest.go` (extend existing — Phase 1.5 harvest implementation lands first per dep).

***Changes from v1:*** *Detection now reads `act.role` config alongside origin-match, removing v1's reliance on origin-match alone (synthesis C1 follow-through). "Harvest skipped" stderr string pinned to a literal (synthesis C4 audit).*

### Ticket 9: doctor extensions — three-state reconciliation + slow-writes + upstream-drift

The doctor side of Phase 2 visibility.

**Blocked-by.** Ticket 3b (consumes the `.act/.slow-writes` schema) AND ticket 6a (consumes the `.act/.sync-log` schema and the `act remote sync` suggestion). Ticket 6b not strictly required — case (h) detection reads file/config state, not the trigger path.

**Scope.**

- Add cases (a'), (c'), (f), (g), (h) detection per the brief's doctor table.
- New `--no-fetch` flag to skip the inline fetch (preserves Phase 1's offline doctor behavior).
- Remote-status block in JSON output: `remote_reachable`, `local_unpushed_count` (case f), `upstream_drift_commits` (case h), `slow_writes_last_hour`, `fetch_failure_reason` (case g).
- Exit-code mapping: case (g) → exit 4 unless `--no-fetch`; cases (f) and (h) → warn (exit 0 with stderr findings unless `--strict`).

**Acceptance criteria.**

- Doctor on a project with two unpushed local commits flags case (f) and lists the issue ids in JSON `local_unpushed_count=2` and stderr line `local: 2 unpushed commits ahead of origin`.
- Doctor with `origin` unreachable flags case (g) and exits 4. Stderr contains the literal string `remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity`. (Synthesis C4 W2 — was unpinned in v1.) Under `--no-fetch`, the case is reported as a warning and the exit is 0.
- Doctor with `origin-upstream` 60 commits behind `origin` flags case (h) and the stderr line contains literal `upstream: origin-upstream is 60 commits behind origin; run 'act remote sync'`.
- Doctor's `.act/.slow-writes`-based summary correctly counts entries within the last hour. Case (g) summary asserts against the schema pinned in ticket 3b — `timestamp` parsed as RFC3339, entries newer than `time.Now().Add(-1 * time.Hour)` counted. Test fixture writes 5 records spaced over 2 hours and asserts the summary shows 2 (the recent half).

**Test plan.** Extends the Phase 1 doctor test suite. Each new case gets its own `TestDocClaim_*` registry entry — five entries total. Slow-writes count test uses synthetic JSON-lines records written directly to `.act/.slow-writes` to avoid the fault-injection roundtrip.

**Files touched.** `internal/cli/doctor.go`, `internal/cli/doctor_test.go`, `docs/spec-v2.md` (doctor case extensions), `internal/cli/docs_sweep_test.go` (five registry entries).

***Changes from v1:*** *Case (h) stderr suggestion pinned to a literal string (synthesis C4 W2). Case (g) summary now asserts against the slow-write schema pinned in ticket 3b (synthesis C2 follow-through). Case (g) stderr text pinned to a literal.*

### Ticket 10: `/orchestrate` doc updates + workflow integration

The workflow doc captures the new lifecycle. **Symlink trap — see below.**

**Blocked-by.** Tickets 7, 1a, 1b, 6a (doc references the subcommands these ship; doc-claim tests rely on the literal subcommand names being stable).

**Symlink trap (synthesis S7).** `~/.claude/commands/orchestrate.md` is a symlink to a file in Andrew's `claude-config` repo (`~/Workspace/claude-config/commands/orchestrate.md`). A worker dispatched into an act-repo worktree that edits this file edits the symlink target — outside the worktree. Result: the change isn't included in the worker's act-repo commit, and it isn't committed-and-pushed in the claude-config repo either, so it never reaches Andrew's other machines. **Ticket 10's bundle MUST include the cross-repo commit-and-push step.** This is enforced in the ticket's acceptance criteria below; the ticket cannot close until the claude-config-side commit lands on its remote. The ticket's worker prompt template includes the explicit `cd ~/Workspace/claude-config && git add commands/orchestrate.md && git commit -m '<msg>' && git push` sequence.

**Scope.**

- Update `~/.claude/commands/orchestrate.md` (via the symlink target: `~/Workspace/claude-config/commands/orchestrate.md`) to use `act bootstrap-worker --from-remote` instead of copy-based bootstrap for remote-attached dispatches.
- Document the cutover: `act remote enable` is the one-time per-project step; after that, `/orchestrate` is unchanged from the operator's perspective.
- Document the failure modes (remote unreachable, slow filesystem, push exhaustion) with the operator-side recovery actions.
- Add a "Phase 1.5 → Phase 2 cutover" section in `docs/migration-runbook.md` explaining when to run `act remote enable` and what changes.

**Acceptance criteria.**

- `~/Workspace/claude-config/commands/orchestrate.md` contains a section that names `act bootstrap-worker --from-remote` as the bootstrap step for remote-attached dispatches.
- The claude-config repo has a commit containing the above change, and that commit has been pushed to the claude-config remote (asserted by checking `git status` on the claude-config repo shows clean and `git rev-parse @{u}` matches HEAD).
- The act repo's commit for ticket 10 references the claude-config commit SHA in its body so the cross-repo link is traceable.
- `TestDocClaim_*` registry entries for: the orchestrate-doc dispatch step (asserts the orchestrate.md content contains the literal `act bootstrap-worker --from-remote`), the cutover instruction, the failure-mode documentation.

**Test plan.** Doc-claim sweep — the act-side test reads the symlink target via `os.Readlink` and asserts on its contents. No runtime behavior tested.

**Files touched.** `~/Workspace/claude-config/commands/orchestrate.md` (live edit via the symlink, with the cross-repo commit-and-push step), `internal/skill/SKILL.md` (worker-protocol section update — in the act repo), `docs/migration-runbook.md` (Phase 2 section). The act-repo bundle includes the symlink read-through-but-write-elsewhere note in the worker prompt template.

***Changes from v1:*** *Symlink trap addressed explicitly (synthesis S1). Worker prompt template now includes the cross-repo commit-and-push step. Acceptance criteria asserts both repos' state, not just the file content.*

### Ticket 11: end-to-end integration test suite

The integration cover.

**Blocked-by.** Tickets 7, 9 (uses bootstrap-worker and asserts doctor output). Practically, 11 lands last.

**Scope.** Comprehensive E2E tests, one per major behavior:

- Two-machine round-trip (simulated by two `.act/.git` clones on a single test host via `BareRemote`, both pointing at a third bare-repo fixture as their `origin`).
- Push contention under high concurrency (4 parallel workers, 50 ops each, all succeed without loss).
- Upstream drift: orchestrator writes 60 ops without sync; doctor flags case (h); `act remote sync` clears.
- Slow filesystem: 5 ops with 2-second injected commit delay (via the `ACT_TEST_SLOW_COMMIT_MS` hook from ticket 3b); `.act/.slow-writes` accumulates; doctor surfaces the summary.
- Dispatch loop: simulate `/orchestrate` issuing two parallel bootstrap-workers, each running an `act create` + `act close` cycle; orchestrator's post-receive triggers fire; upstream sync log shows both events.

**Acceptance criteria.** Each behavior has at least one passing E2E test. CI runtime budget: ~2 minutes total (each test sub-minute).

**Test plan.** Lives in `internal/integration/phase2_test.go` (new package). Uses `t.Parallel` aggressively. Consumes `internal/testfixtures/remote.go` from ticket 2.

**Files touched.** New test package only.

***Changes from v1:*** *Consumes `internal/testfixtures/remote.go` from ticket 2 (synthesis C6). Slow-filesystem case uses the fault-injection env hook named in ticket 3b. CI-budget trim and dispatch-order tweaks explicitly out of scope per the v1 synthesis (see ticket description's out-of-scope list).*

---

## Dependency graph

```
                    act-b77a80 (Phase 1.5, hard dep)
                           |
                    Ticket 1a (foundation + config + enable/disable + hook skeleton)
                   /                |                    \
              Ticket 2          Ticket 1b              Ticket 7
              (helpers +     (add-upstream;            (bootstrap-worker;
               fixtures)      parallel w/ Phase 2)      consumes 1a's config schema)
                  |                                          |
              Ticket 3a (push-on-write)                      |
                  |                                          |
                  +--- Ticket 5 (read cache; rebases on 3a, same file)
                  |
                  +--- Ticket 3b (offline + slow-writes; rebases on 3a)
                  |
                  +--- Ticket 6a (sync subcommand + hook content; blocked by 1a)
                  |          |
                  +--- Ticket 6b (orchestrator-write trigger;
                  |              blocked by 3a + 6a; rebases on 5)
                  |
                  +--- Ticket 8 (harvest narrowing; blocked by 7)
                  |
                  +--- Ticket 9 (doctor; blocked by 3b + 6a)
                  |          |
                  +--- Ticket 10 (orchestrate doc; blocked by 7/1a/1b/6a)
                  |
                  +--- Ticket 11 (E2E; blocked by 7 + 9)
```

The four new explicit edges (synthesis): (1) `act-b77a80 → 1a` as a hard dep, not a risk-list bullet; (2) `3a → 5` because both edit `internal/gitops/gitops.go`; (3) `1a → 1b` because they share `internal/cli/remote.go`; (4) `3a → 6b` because 6b extends the actGitOps post-commit hook 3a establishes.

Sequential bottlenecks: `act-b77a80` → 1a → 2 → 3a → {5, 3b, 6a} is the critical path through Phase 2 setup. After 3a lands, several tickets fan out — 5 and 3b rebase on 3a (same file, sequential), but 6a and 8 are file-disjoint and run in parallel.

Parallelization opportunities under `/orchestrate`:

- After `act-b77a80` lands: 1a single-worker.
- After 1a lands: 2, 1b, 7 in parallel (three workers; provably disjoint files).
- After 2 lands: 3a (single worker — write paths touch many files, high merge risk).
- After 3a lands: 5 (rebases on 3a), 3b (rebases on 3a), 6a (file-disjoint), 8 (after 7 lands), 6b (after 6a lands and 5 rebases) — sequenced by file overlap on `internal/gitops/gitops.go`: 3a → 5 → 3b → 6b in that file; 6a/8 in parallel with the gitops sequence.
- After 3b and 6a land: 9 (doctor).
- After 9 lands: 10 (doc) and 11 (E2E) in parallel.

Worst case wall-clock with serial dispatch: ~13 dispatch cycles. With aggressive parallelism (respecting the gitops-file-overlap sequencing): ~8 cycles. At ~1-2 cycles/day, this is 6-12 working days of orchestrate time — slightly more than v1's estimate because the gitops-file sequencing is now explicit instead of implicit.

## Open implementation questions

Plan-stage details that don't gate the plan but need to be settled before the relevant ticket starts:

1. **Worker telemetry coupling.** The brief flags this as a remaining open question. For this plan, scope it out — leave as a follow-up after Phase 2 ships.

(v1's OQs #1 — slow-write log format — closed by ticket 3b's pinned schema. OQ #2 — `.act/.sync-log` retention — closed by ticket 6a's "capped at 100 entries, same pruning shape as `.slow-writes`" line. OQ #4 — orchestrator detection — closed by ticket 1a's `act.role` config key.)

## Risks and rough-edges

- **Concurrent write path edits.** Tickets 3a, 3b, 5, and 6b all edit `internal/gitops/gitops.go`. The dep graph sequences them; the largest serialization point in the plan.
- **`updateInstead` cross-platform behavior.** Validated on macOS (Andrew's machine). Linux should be identical; Windows is untested (and Phase 1 has open Windows tickets — `act-2f3d` for op filename colons). The plan does not address Windows; it inherits Phase 1's status.
- **Test-fixture remotes.** Ticket 2 owns `internal/testfixtures/remote.go` and picks the bare-repo-on-filesystem shape. Tickets 1b/6a/6b/7/11 import. Settling this in ticket 2 avoids each consumer reinventing.
- **Symlink ownership for ticket 10.** `~/.claude/commands/orchestrate.md` is a symlink into Andrew's claude-config repo. Ticket 10's bundle includes the cross-repo commit-and-push step; the act-side test asserts both repos' state.
- **Phase 1.5 dependency.** Now a hard dep edge (`act-b77a80 → 1a`) in the graph above, not a risk-list bullet. An orchestrator dispatching 1a will see the dep and refuse until Phase 1.5 lands.

## Cross-references

- Design brief v4: `docs/coordination-plane-phase2-design.md` (commit `a7f1bd1`).
- Phase 1 design: `docs/coordination-plane-design.md` v2.1.
- Phase 1.5 umbrella: `act-b77a80` (hard dep — see graph).
- v1 reviews and synthesis: `docs/reviews/plan-v1-cold-eye-2026-05-18.md`, `docs/reviews/plan-v1-architect-2026-05-18.md`, `docs/reviews/plan-v1-synthesis-2026-05-18.md`.
- Project conventions: `CLAUDE.md` documentation discipline section; commit-marker trailer rule.
