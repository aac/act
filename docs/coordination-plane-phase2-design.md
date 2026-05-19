# Coordination plane — Phase 2 design brief

**Status:** v3 draft, awaiting narrow remediation-check review.
**Author:** drafted in session 2026-05-18; revised through two review gates.
**Supersedes:** v2 (commit `4776643`), v1 (commit `00c4215`).
**v3 changes** (closes second-gate findings): post-push reachability verification mandated; `updateInstead` silent-rejection failure mode owned; GitHub upstream lifecycle committed to `act remote sync` (no hook, no cron); fold-checkpoint invalidation on rebase stated; harvest explicitly orchestrator-initiated; doctor case-(f) detection mechanism specified; bootstrap-over-SSH knobs (shallow, timeout, atomic) named; `push_exhausted` / `remote_unreachable` spec-v2.md and `TestDocClaim_*` dependencies called out; dispatch-mode signal named (`ACT_DISPATCH_MODE`); read-cache state location committed; migration-UX non-question dropped.
**v2 changes from v1:** topology committed (orchestrator's `.act/.git` is canonical, no separate bare repo); push-contention loop rewritten (rebase not merge); harvest scope narrowed; glossary, trust model, clock-skew, op-log growth, doctor three-state extension all added; implementation fanout removed.
**Builds on:** Phase 1 design at `docs/coordination-plane-design.md` v2.1 (as-built in `f3d9945`..`3298840`), plus the Phase 1.5 worker-isolation pivot currently in tickets `act-12dc23`, `act-9fadf0`, `act-9e7078`, `act-c8028f` (umbrella `act-b77a80`).

## Glossary

Terms used throughout. Phase 1 readers can skip.

- **op file.** An append-only state-change record in `.act/ops/`. One per logical mutation (create, claim, close, dep-add, etc.).
- **HLC.** Hybrid Logical Clock — a `(physical_ns, logical_counter, node_id)` tuple stamped on each op. Provides a total order across writers without requiring synchronized wall clocks. Tolerates skew within a bound (the largest gap any node has seen, plus a safety margin).
- **HLC + nonce filename.** Each op file's name encodes its HLC plus a random nonce, guaranteeing globally unique filenames across concurrent writers without coordination.
- **op_hash.** SHA-256 of the op file's canonicalized content. Used as a deterministic LWW tiebreaker when two ops have identical HLC (vanishingly rare; see `hlc.Stamp`, act-492e).
- **fold.** Deterministic replay of ops to materialize current state. Same op set → same state, regardless of file-system order.
- **LWW (last-write-wins).** Semantic conflict resolution at fold time: when two ops touch the same logical state (e.g., both claim the same issue), the op with the later HLC wins. `op_hash` breaks ties.
- **Phase 1.5.** The worker-isolation pivot landing now: copy `.act/` to each dispatched worker, harvest new ops back at teardown. Bridges between Phase 1 (single-writer) and Phase 2 (shared remote, continuous sync). Tracked under umbrella ticket `act-b77a80`.
- **brain/hands.** A worker topology where one Claude session ("brain") dispatches sandboxed agents ("hands") with restricted file-system access — typically in containers or remote VMs without filesystem share back to the host.
- **`actGitOps` / `hostGitOps`.** Two git-ops handles in the codebase. `actGitOps` writes to the nested `.act/.git`; `hostGitOps` reads work commits from the host repo for marker scans. The dual-handle refactor landed in Phase 1 (`f3d9945`).
- **Act-Id trailer.** A commit-body trailer like `Act-Id: act-XXXXXX` on work commits. The contract between act and the host repo's history (act-c4c5).

## Problem statement

Phase 1 + 1.5 ships a usable coordination plane for the current single-machine dogfood load (≤4 concurrent agents per `/orchestrate` pass, staleness window in minutes, harvest at teardown). It explicitly does *not* support:

1. **Cross-worker visibility during execution.** A worker creating a ticket mid-flight isn't visible to peer workers until both have torn down and harvested. Acceptable today; ceiling under heavier dispatch.
2. **Multi-machine work.** The nested `.act/.git` has no remote. State is captive to whichever machine the orchestrator is running on.
3. **Sandboxed workers without filesystem share.** Brain/hands containers, remote VMs, future Claude Agents managed environments — none can participate without out-of-band copy.

Phase 2 closes all three with a single mechanism: each worker gets its own clone of the coordination plane, pushes ops as it writes them, fetches peers' ops on read. Harvest stays in the codebase but its scope shrinks (see "Harvest in Phase 2").

## Trust model and target use case

**Target:** one human (Andrew) coordinating one or more agents across one or more machines under his control. This is the widest scope Phase 2 plans for. Specifically *not* in scope:

- Team collaboration (multiple humans writing acts on the same project). Deferred to a later phase once a real teammate exists.
- Multi-tenant remotes serving unrelated projects.
- Adversarial agents or untrusted code paths writing acts.

**Auth model:** none beyond filesystem and SSH. Workers reach the orchestrator's `.act/.git` over local filesystem (same-machine workers) or SSH (remote-machine workers). Whichever credentials git already has are sufficient. There is no project-level auth, ACL, or signing. Acceptable because every participant is under the operator's control.

**Op-log content sensitivity:** ticket descriptions can contain code references, paths, and occasional pasted credentials. The op log is therefore as sensitive as the source tree. Anything that backs up the source tree should back up `.act/.git`. If the optional GitHub upstream is enabled (see Topology), it must be a private repo. Doctor will warn if the upstream is configured public.

## What Phase 2 inherits from Phase 1 (load-bearing properties)

- **HLC + nonce filenames** → two writers never collide on op-file paths; set-union merge of two `ops/` directories is exact (the union is itself a valid `ops/` state because each file is immutable and globally unique-named).
- **Append-only ops** → no op file is ever edited or deleted. No edit-conflicts. The only mutable thing in the entire `.act/.git` is the branch ref, which fast-forwards trivially under HLC-stamped ordering (see Push contention).
- **Deterministic fold with HLC LWW** → semantic conflicts resolve at fold time without coordination. Two workers claiming the same issue: HLC LWW picks one; the other gets `claim_lost` on its next read.
- **`Act-Id:` trailer on work commits** → host repo history carries enough provenance for coordination context independent of `.act/.git` state. Phase 2 does not modify this contract.

## Goals

1. Workers push and fetch coordination state to a shared remote during execution.
2. Sandboxed and remote workers participate equivalently to local workers (modulo the sandboxed-no-network case, where harvest still applies — see Harvest in Phase 2).
3. The coordination plane survives single-machine failure when the optional GitHub upstream is configured. Without the upstream, it's as durable as the host machine's filesystem.
4. Migration from Phase 1 + 1.5 is mechanical and reversible: one command turns it on, one command turns it back off.

Latency target (informal SLA): a write on machine A is visible to a fresh read on machine B within ~5 seconds, network permitting. This number is what the cache TTL is sized against; it's the loosest acceptable for orchestrator dispatch (where a 10-second window means duplicate-claim work but no correctness violation).

## Topology — orchestrator's `.act/.git` is canonical

There is one git repo for the coordination plane on the orchestrator machine: `<project>/.act/.git`. Workers (local worktrees, remote sandboxes, brain/hands containers) clone from it and push back. No separate bare repo exists.

**Configuration on the orchestrator's `.act/.git`:**

```
git config receive.denyCurrentBranch updateInstead
```

This setting permits pushes to the currently checked-out branch, with the orchestrator's working tree updating automatically iff the working tree is clean. The dirty window in practice is sub-second (between `act` writing an op file and committing it).

**Worker clone:**

```
git clone <orchestrator>/.act/.git <worker-tree>/.act/.git
```

For same-machine workers, `<orchestrator>` is an absolute path. For remote workers, it's `ssh://user@host/path/to/.act/.git`. Standard git URL semantics; no new transport.

**Optional GitHub upstream** for durability and cross-machine convenience: the orchestrator's `.act/.git` has GitHub configured as a second remote (e.g., `origin-upstream`). State replication is by an explicit `act remote sync` call invoked by the orchestrator's dispatch loop after each successful worker push. **Not** a post-commit hook (would block worker writes on slow GitHub network and could fire mid-rebase during contention retries, publishing partial state). **Not** cron (multi-minute durability gap defeats the "as durable as the upstream" claim). Workers do not interact with the upstream directly — they only talk to the orchestrator's `.act/.git`. If the orchestrator's machine is gone, recovery is "clone the GitHub upstream, reconfigure as the new orchestrator."

The dispatch loop's responsibility for `act remote sync` is documented in `/orchestrate` and is fail-soft: a failed upstream push warns but does not block dispatch — durability is best-effort, not synchronous.

**Alternatives considered and rejected:** (a) a separate bare repo at `~/.local/share/act/<project>/repo.git` — XDG-canonical but adds a second location to track, complicates backup, and provides no benefit since the orchestrator's `.act/.git` is already the source of truth; (b) GitHub-required as primary — forces every read and write through network latency, breaks offline operation, doesn't fit the single-user-many-agents target.

## Sync protocol — eager writes, cached reads

Writes push synchronously. Reads fetch with a TTL cache. Specific commands override read caching to fetch unconditionally.

**Write path:** `act create`/`close`/`update`/`dep add` commits to the local clone, then immediately `git push origin <branch>`. On non-fast-forward, retry (see Push contention). The write does not return success until push lands. Failure modes:

- Network unreachable → fail with `remote_unreachable` error code, suggest `--offline` (queues op locally, retries on next reachable write).
- Push rejected after exhausted retries → fail with `push_exhausted` error code, distinct from transient errors.
- Local commit succeeded but push failed → op file exists locally; recovery via the harvest fallback (see Harvest in Phase 2).

**Read path:** `act show`/`ready`/`log` checks the local fetch timestamp. If fetched within TTL window, read local state directly. If stale, `git fetch && git rebase origin/<branch>` first, then read. **Post-rebase invariant:** any `.act/fold-checkpoint.json` and `.act/index.db` MUST be treated as stale; the next fold re-verifies against the current tree hash before relying on cached state.

**Cache state location.** The "last fetch" timestamp is the mtime of `.act/.git/FETCH_HEAD` — written by git on every successful fetch, no sidecar file needed. TTL is read from `.act/.git/config` key `act.readCacheTTLSeconds` (default 5).

**Cache invalidation triggers** (cache is considered fresh if any of):

1. mtime of `.act/.git/FETCH_HEAD` is within TTL.
2. A local write happened more recently than the last fetch (the writer's own push is by definition fresher than any pre-push fetched state). Writes unconditionally invalidate the local read cache by touching FETCH_HEAD-or-equivalent on success.

**Commands that bypass the TTL cache** (always fetch):

- `act doctor` — diagnostic by nature; always wants ground truth.
- `act ready` when invoked by `/orchestrate` in dispatch mode. The orchestrator sets `ACT_DISPATCH_MODE=1` in the env of its `act ready` invocation; the act binary reads this env var and bypasses the cache. A stale `act ready` here causes duplicate-claim attempts that waste work even if LWW resolves them.
- Anything passed `--fresh` or `--no-cache`.

**Alternatives considered and rejected:** (a) eager-everywhere (push on write, fetch on every read, no cache) — pays network latency per command, breaks offline reads, slows doctor sweeps significantly; (b) lazy daemon-based sync — adds a moving part, makes "did my close publish?" a real question, harder to diagnose failures.

## Push contention — fetch, rebase, push, verify

Two workers commit ops with non-overlapping filenames (HLC+nonce ensures this) and both push to the same ref. Whichever pushes first wins; the second gets non-fast-forward. The retry loop is:

1. `git fetch origin <branch>`.
2. `git rebase origin/<branch>` — replay local commits on top of the fetched state. Because each commit adds new files in disjoint paths and edits nothing, rebase produces zero conflicts.
3. `git push origin <branch>`.
4. **Post-push reachability verification.** `git fetch origin <branch>` and `git merge-base --is-ancestor <local-HEAD> origin/<branch>`. If the local commit is reachable from the remote ref, push succeeded. If not, the push was silently rejected — go to step 2.
5. After N (default 5) full retries with exponential backoff capped at 1s, abort with `push_exhausted`.

**Why rebase, not merge:** preserves linear history in the coordination plane. Merge commits in the op log are noise — the ops themselves carry HLC, so reconstructing causality from merge topology isn't needed. Linear history makes log inspection and bisect (for the rare case of a corrupt op surviving fold) trivial. Specifically, `git merge --ff-only` would always fail after the first concurrent push because divergent histories aren't fast-forwards.

**Why the reachability check is mandatory.** Git's push can return exit 0 in two cases where the remote ref did not advance: (a) network drops between object transfer and ref-update confirmation; (b) `receive.denyCurrentBranch=updateInstead` silently rejects the push when the orchestrator's working tree is dirty (a worker push arriving during the orchestrator's own sub-second write window). Without the reachability check, a worker's retry loop treats both as success and proceeds with the worker believing its ops are published when they are not. The check makes the silent-rejection class observable and triggers retry. This is the same mechanism that handles the network-drop case — both are recovered identically.

**`updateInstead` as the cross-write serialization mechanism.** When the orchestrator runs its own `act` commands, the same working tree is touched as is the target of inbound worker pushes. `updateInstead` is the lock: it refuses pushes while the working tree is dirty, and refuses local writes from completing inconsistently while a push is in progress. Workers observe push refusal via step 4 and retry. Orchestrators do not need an explicit lock around their own write path because their writes don't push (they're already on the canonical) — they only need to commit fast enough that the dirty window doesn't widen pathologically. Doctor adds a check: if any `act` write takes more than 1s wall-clock between op-file stage and commit, warn (indicates filesystem trouble — slow NFS, encrypted overlay, etc.).

**Partial push failures.** Git's object transfer is not atomic with the ref update. A worker can transfer objects successfully, then have the ref update fail. The retry loop's step-1 fetch reveals whether the prior push's objects arrived; if they did, the worker's rebase becomes a no-op (its commits are already in `origin/<branch>`). If they didn't, the retry transfers them again. Re-transferring already-arrived objects is harmless (git deduplicates on the receiver). The N-retry cap bounds spin risk.

**Mutable state in `.act/.git`:** only the branch ref. No config, hooks, or working-tree state is touched by ops. Index file is reset between operations. This makes the rebase safe under any concurrent state.

## Per-worker clone lifecycle

Replaces Phase 1.5's copy-on-dispatch for remote-attached workers (workers that can reach the orchestrator's `.act/.git`):

1. **Dispatch.** Orchestrator runs `act bootstrap-worker --from-remote <orchestrator-url> <worktree>`. The subcommand: (a) clones with `--depth 1` (coordination plane only needs current state — fold replays op files, not git history, so deep history is dead weight); (b) clones into a temp directory under `<worktree>/.act.bootstrap/`, then atomically renames to `<worktree>/.act/` on success — a partial or stalled clone leaves no debris; (c) honors `act.bootstrapTimeoutSeconds` from `.act/.git/config` (default 30s) and aborts cleanly on timeout; (d) validates the resulting state with a round-trip `act ready` before returning success.
2. **Execution.** Worker calls `act` commands normally. Writes push to origin (the orchestrator's `.act/.git`). Reads fetch within TTL. The worker never knows it's a worker.
3. **Cross-worker discovery.** Worker B's `act ready` after worker A's `act close` of issue X (with fetch cache cold) sees the close. Latency window is min(TTL, time since last fetch). For TTL=5s, typical worker-to-worker visibility is sub-10s.
4. **Failure.** Worker dies mid-execution. If its last write was successfully pushed, no recovery needed — the op is on the orchestrator. If a local commit hadn't yet pushed (network blip, process kill), harvest the worker's `.act/.git` from main before tearing down the worktree (see Harvest in Phase 2).
5. **Teardown.** Worker's `.act/.git` is discarded with the worktree. All confirmed-pushed state is already on the orchestrator.

**Sandboxed-no-network workers** follow the Phase 1.5 path: copy `.act/` in via `act bootstrap-worker` (no remote), worker writes locally, harvest at teardown. They do not push; their ops only land on the orchestrator at harvest time. Documented separately so callers can choose explicitly.

## Doctor in the three-state world

Phase 1 v2.1 tabulated five reconciliation cases between code and act state. Phase 2 adds the remote as a third state. Most Phase 1 cases gain a sub-case "...and the remote has it":

| Phase 1 case | Phase 2 extension |
|---|---|
| (a) marker in code, no matching issue locally | Sub-case (a'): no local, but remote has it → fetch and converge. Becomes the common case during a worker's execution. |
| (b) issue locally, no marker in code | Unchanged. The issue is genuine, just no work has landed. |
| (c) marker and issue both present, status mismatched | Sub-case (c'): local and remote disagree on status → HLC LWW picks the winner; doctor logs the resolution. |
| (d) closed issue, no marker | Unchanged. |
| (e) marker present, issue closed | Unchanged. |
| (new — Phase 2) | (f) issue exists locally, never pushed to remote → flag, suggest manual push or harvest. Detection: `git diff-tree origin/<branch>..HEAD -- .act/ops/` non-empty after a fresh fetch. |
| (new — Phase 2) | (g) remote ahead of local by more than TTL after a fetch attempt failed → flag remote-unreachable. Detection: fetch returns non-zero or hangs past `act.fetchTimeoutSeconds` (default 10s). |

Doctor in Phase 2 fetches before running checks (unless `--no-fetch`) and includes a remote-status block in its output. The full table extension lives in the implementation-side doc once a plan exists; this brief commits to the shape and the detection mechanism for each new case.

**Exit-code mapping.** Case (f) is severity warn (exit 0 with stderr finding); case (g) is severity error (exit 4, matching Phase 1's `remote_unreachable` envelope) unless `--no-fetch` was passed, in which case it's downgraded to warn.

## Clock skew and op-log growth

**Clock skew bound.** HLC tolerates skew up to whatever delta is between any two participants' wall clocks plus the logical counter range. Practically: as long as no participant's clock is off by more than a few minutes from any peer, HLC ordering remains consistent. Doctor adds a check: if any op has an HLC physical time > now + 10 minutes (configurable), warn loudly — it's either a future-dated op (mis-set clock somewhere) or a corrupt write. The check existed in Phase 1 for backdated ops; Phase 2 extends it forward.

**Op-log growth.** Append-only by design; no compaction in Phase 2. Sizing analysis: a typical op file is ~500 bytes. At 100 ops/day (heavy single-user dogfood), one year of state is ~18MB plus git pack overhead. Acceptable as-is. If a project's op log ever crosses a soft threshold (say 100MB), the answer is `git gc --aggressive` on `.act/.git`, which is standard git maintenance. No act-side compaction is planned; ops are addressable forever for audit and replay.

## Migration from Phase 1 + 1.5

Preconditions:

- The project has completed Phase 1 migration (`.act/.git` exists, host `.act/` is gitignored, pre-commit hook in place). Verified by `act doctor --check nested-layout` returning zero findings. Projects still on the pre-Phase-1 layout must run `act migrate-to-nested` first.
- The orchestrator machine is identified — the machine whose `.act/.git` will be the canonical.

Steps:

1. On the orchestrator: `act remote enable`. The subcommand sets `receive.denyCurrentBranch=updateInstead` on `.act/.git`, sets `act.readCacheTTLSeconds` and related config keys to their defaults, and runs a doctor pass to confirm the result.
2. Optionally, configure the GitHub upstream: `act remote add-upstream <github-url>` — adds the URL as a second remote and does an initial push.
3. Update `/orchestrate`'s dispatch path to use clone-based worker bootstrap (`act bootstrap-worker --from-remote ...`) instead of copy. The orchestrate doc gains a "Phase 1.5 → Phase 2 cutover" note; copy-on-dispatch is deprecated for remote-attached workers but stays in the codebase for sandboxed-no-network workers.
4. From this point on, the project is on Phase 2. Reverting: `act remote disable` (unsets the config keys, optionally removes the upstream remote), revert orchestrate to copy-based dispatch. All ops continue to fold correctly — Phase 2 doesn't change the op format.

**Worktree regression resolved.** Under Phase 1 + 1.5 alone, `git worktree add` doesn't carry `.act/`, so worker agents can't run `act` commands (the regression documented in the session handoff at `f3d9945`..`3298840`). Phase 2's clone-on-dispatch path solves this: every worktree gets its own `.act/.git` clone via the bootstrap subcommand, no longer dependent on filesystem layout.

## Harvest in Phase 2

Harvest is not retired. It is scoped:

- **In scope for harvest:** (a) sandboxed-no-network workers (containers, remote VMs without orchestrator reachability) — their workflow remains copy-on-dispatch + harvest-on-teardown; (b) crash recovery — a worker that local-committed but never successfully pushed before dying. Harvest pulls those ops back from the worker's `.act/.git` before worktree teardown.
- **Out of scope for harvest:** normal teardown of remote-attached workers. Those workers pushed during execution; by teardown, the orchestrator already has everything. No-op the harvest call on those workers' teardown.

**Initiation side.** Harvest is always orchestrator-initiated. The worker (or its filesystem mount) is a passive op-source — for no-network sandboxed workers, the worker cannot fetch to diff against the orchestrator, so it cannot run harvest itself. The orchestrator inspects the worker's `.act/ops/` directory directly (over filesystem or rsync-over-ssh for sandboxed cases), diffs against its own op set, and copies only the delta.

**Idempotency requirement (test-enforced).** Harvest of a worker whose ops already landed on the orchestrator via push must be a no-op. Detection: op-set diff between the worker's `.act/ops/` and the orchestrator's. Same set → no-op. Different set → copy the difference. The HLC+nonce filename guarantee makes the diff exact and unambiguous.

**`--push-every-op` flag** for sandboxed workers that do have network: forces each `act` write to push immediately rather than batching. Tradeoffs network cost for crash-resilience. Off by default; opt-in for workers in environments where harvest isn't practical.

## Resolved open questions

From v1, these are now closed:

- **Bare repo location** — moot, no bare repo. Orchestrator's `.act/.git` is the canonical.
- **Cache TTL tuning** — 5s default, configurable per project via `.act/.git/config`, with explicit override list for dispatch-path commands.
- **Auth model** — explicitly single-user-machines-under-operator-control, SSH + filesystem only. Trust section above.
- **Orphan worker clone GC** — op-set diff between remote and worker clone is the detection mechanism. No worker manifest needed. Data loss when a sandbox is wiped before first push is accepted; `--push-every-op` mitigates for workers that have network.

## Spec and test-discipline dependencies

Phase 2 introduces two new error codes that callers (CLI users, agents parsing envelopes) will rely on: `push_exhausted` (distinct from transient errors; retry-limit exhaustion) and `remote_unreachable` (network-level failure, transient). Per the doc-discipline rule in this repo's `CLAUDE.md`, every user-visible behavior claim requires an asserting test at the user-visible boundary, and the spec must enumerate it.

Concretely, this means the Phase 2 implementation must, in the same commits that introduce the codes:

1. Add `push_exhausted` and `remote_unreachable` to the error-envelope code table in `docs/spec-v2.md` with exit-code mappings (proposed: exit 4 for both, distinguished by code+`details`).
2. Add `Envelope.Code` constants for both in `internal/cli/errors.go`.
3. Register `TestDocClaim_PushExhaustedCode` and `TestDocClaim_RemoteUnreachableCode` in `internal/cli/docs_sweep_test.go`, asserting the codes appear in CLI output on the failure paths they describe.

The brief flags this dependency so plan-writing can scope it correctly; it is not a separate ticket but a constraint on every ticket that touches a write path or doctor output.

## Remaining open questions

These need plan-stage decisions but don't gate the design:

1. **Doctor's remote-status block format.** JSON shape and human-readable rendering. Plan-stage detail; the shape is informed by the existing Phase 1 doctor output.
2. **Worker telemetry.** Should the orchestrator log when a worker's `act` command times out fetching, to surface flaky network early? Probably yes; needs a small instrumentation hook somewhere in the dispatch loop.

## Cross-references

- Phase 1 design: `docs/coordination-plane-design.md` v2.1.
- Phase 1 implementation, key commits: dual-handle GitOps refactor (`f3d9945`), nested-repo bootstrap (`c1b4`), Act-Id trailer (`c4c5`), HLC Stamp (`act-492e`), migration tool (`3298840`).
- Phase 1.5 implementation tickets: bootstrap-worker, harvest, worker-protocol, round-trip tests (umbrella ticket `act-b77a80`).
- v1 of this brief: commit `00c4215`. Both review responses captured in the session log for this conversation.
