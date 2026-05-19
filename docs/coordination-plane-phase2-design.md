# Coordination plane — Phase 2 design brief

**Status:** draft, awaiting review (first design-review gate).
**Author:** drafted in session 2026-05-18.
**Supersedes:** nothing yet. Builds on Phase 1 (`docs/coordination-plane-design.md` v2.1, as-built in commits `f3d9945`..`3298840`).

## Problem statement

Phase 1 ships a nested `.act/.git` repo per project, with the host repo's working tree gitignoring `.act/`. Coordination state is decoupled from code, work commits carry `Act-Id:` trailers, and the design supports multiple concurrent writers in principle (HLC+nonce op filenames, append-only ops, fold-time LWW with `hlc.Stamp`).

In practice, Phase 1 plus the Phase-1.5 copy-on-dispatch / harvest-on-teardown lifecycle (act-12dc23, act-9fadf0, act-9e7078) is sufficient for the current dogfood load: one machine, ≤4 concurrent agents per `/orchestrate` pass, snapshot-staleness window measured in minutes. Workers operate on local `.act/` copies; the orchestrator harvests at teardown.

What Phase 1+1.5 explicitly does not do:

1. **No cross-worker visibility during execution.** Worker A doesn't see worker B's new tickets until both harvests complete. For long-running work this is a real ceiling.
2. **No remote.** Nested `.act/.git` exists only on the local filesystem of the host machine. A failed disk, a wiped workspace, or a switch to a different machine loses the coordination plane.
3. **No multi-machine support.** Brain/hands sandbox workers in containers, on remote VMs, or in CI environments cannot participate without an out-of-band copy mechanism.
4. **No team-collaboration story.** External contributors writing tickets is not supported.

Phase 2's goal is to close all four, with a specific focus on (1) and (3) because they directly enable the next-tier dogfood scenarios (brain/hands sandboxed workers, multi-machine agent orchestration).

## What we get for free from Phase 1

Phase 2 inherits several properties that make distributed coordination tractable, and the brief should be read with these as given rather than re-litigated:

- **Op filenames are HLC + nonce.** Two writers never collide on op-file path. Set-union merge of two `ops/` directories is exact.
- **Ops are append-only.** No op file is ever edited or deleted. There are no edit-conflicts to resolve; only new files to add.
- **Fold is deterministic with HLC LWW.** Semantic conflicts (two writers touched the same logical state, e.g. both claimed the same issue) resolve by HLC; ties by `op_hash` from `hlc.Stamp` (act-492e). No human in the loop.
- **Work commits carry `Act-Id:` trailers** (act-c4c5). The host repo's commit log is fully introspectable for coordination context independent of the `.act/.git` state.

These properties mean Phase 2 is not "design a distributed database." It is "design a sync protocol for a CRDT-friendly append-only log over git."

## Phase 2 goals, in priority order

1. Workers can pull and push coordination state to a shared remote during execution, not just at lifecycle boundaries.
2. Sandboxed and remote workers participate equivalently to local workers.
3. The coordination plane survives single-machine failure and supports multi-machine work.
4. Migration from Phase 1 is mechanical: existing projects gain a remote URL, nothing else changes for their callers.

Non-goals for Phase 2:

- UI for inspecting coordination-plane state across remotes (Phase 3).
- Multi-tenant remotes serving multiple unrelated projects (Phase 3).
- Anything beyond single-user-many-workers semantics (no team collaboration this phase; that's a separate design once we have a real user).
- Eliminating the harvest mechanism from Phase 1+1.5. Harvest remains as a fallback / sandboxed-worker integration point even after Phase 2 ships.

## Remote topology — three options

### A. Local bare repo

`~/.local/share/act/<project-hash>/repo.git` is the canonical bare repo. Each project's host `.act/.git` and each worker's `.act/.git` clone are remotes-of-remotes pointing at this. Surveys (`/orchestrate`, doctor) can hit the bare repo directly.

Pros: zero external dependencies; fast (local I/O); no auth surface; survives `act` repo deletion if the bare repo lives elsewhere.

Cons: single-machine only by default; bare repo is on the same disk as everything else (no real failure-domain isolation); team-sharing requires SCP/rsync.

### B. GitHub (or any git host) remote

Each project gets a private GitHub repo for its coordination plane, separate from the code repo. Nested `.act/.git` clones push and pull from there.

Pros: real network-attached durability; team collaboration becomes mechanical (share repo access); easy multi-machine; existing auth (SSH keys, GitHub tokens).

Cons: requires GitHub or equivalent; network round-trip per push/fetch; rate limits; one more repo per project to manage.

### C. Hybrid — local bare repo with optional git-host upstream

Local bare repo is the workers' canonical remote. The local bare repo *itself* has GitHub as its upstream and syncs opportunistically (cron, post-commit hook, or on-demand). Workers don't pay GitHub latency on hot paths; durability and multi-machine are available when needed.

Pros: keeps hot-path latency local; durability when wanted; supports the "Andrew is offline but workers are still running" case; the multi-machine case becomes "fetch from your local bare repo's upstream."

Cons: more moving pieces (two layers of syncing); operationally harder to reason about; eventual consistency between local bare and upstream can be subtle.

**Tentative recommendation:** B for the implementation simplicity. C if the brief gets pushback on GitHub-as-required-dependency, since C cleanly accommodates an offline-by-default mode.

## Sync protocol — three shapes

### Eager (push-on-write, fetch-on-read)

Every `act create`/`act close`/`act update` does `git commit && git push` to the remote. Every `act show`/`act ready`/`act log` does `git fetch && git merge` (or `git pull --rebase --ff-only`) before resolving local state.

Pros: simplest mental model; staleness window approaches zero; failures surface immediately.

Cons: every command pays network latency; high-frequency reads (e.g. doctor sweeps) become slow; offline mode is broken unless explicitly handled.

### Lazy (background daemon, opportunistic)

A long-running sync process per worker (or per host) fetches every N seconds and pushes queued local commits in the background. `act` commands operate on local state; sync is decoupled.

Pros: zero hot-path latency; offline-tolerant; can batch pushes for efficiency.

Cons: stale reads possible up to one sync interval; daemon is a new moving part; failure modes (daemon crashed silently) are harder to diagnose; "did my act create actually publish?" becomes a real question.

### Hybrid (eager writes, cached reads)

Writes are eager (push-on-commit, like option Eager). Reads use a cache: if the local fetch happened within last N seconds, skip fetch and read local. Otherwise fetch then read.

Pros: writes have known durability semantics (when `act close` returns, the close is published); reads are fast in the common case (consecutive commands within window); offline reads work, offline writes fail clearly.

Cons: write latency is still per-command; clock-skew between fetch decisions can cause subtle staleness bugs; the cache invalidation policy needs to be tunable per command class (doctor wants fresh data, `act show` is fine stale).

**Tentative recommendation:** Hybrid, with the cache window default at 5 seconds. Specific commands (`act doctor`, `/orchestrate` dispatch) override to fetch-unconditionally. Writes don't return success until push lands; failures surface cleanly.

## Per-worker clone lifecycle

Replaces the Phase-1.5 copy-on-dispatch:

1. **Dispatch:** orchestrator runs `git clone <remote> <worktree>/.act/.git` (or equivalently a single-shot `act bootstrap-worker --from-remote <url>`). Worker's `.act/` is a full clone, with the remote configured.
2. **Execution:** worker calls `act` commands normally. Writes push to remote; reads fetch (within cache TTL). No harvest needed for normal operation.
3. **Failure:** if a worker dies mid-write (commit happened locally, push hadn't yet completed), the orchestrator can recover by harvesting the worker's `.act/` the same way Phase 1+1.5 does. Harvest stays in the codebase as a fallback.
4. **Teardown:** worker's `.act/` is discarded with the worktree. All published state is already on the remote.

The sandboxed-worker variant differs only in how the clone happens: `git clone` over SSH/HTTPS instead of a local path. Same lifecycle, same commands.

## Push contention — what happens when two workers push concurrently

Two workers commit ops with non-overlapping filenames (HLC+nonce ensures this) and both push to the same remote ref. Whichever pushes first wins. The second push gets `non-fast-forward`. The standard recovery is:

1. `git fetch` (pulls the first writer's commit).
2. `git merge --no-ff` (or `--ff-only` if rebase is preferred). Because filenames are disjoint and content is append-only, there are zero conflicts to resolve.
3. Retry push.

This is the same loop a distributed git wiki uses. Build it into the `act` push helper as a transparent retry with bounded attempts (e.g., 5 retries, exponential backoff up to 1s). The HLC LWW resolves any semantic races at fold time, not at git-merge time.

## Migration from Phase 1+1.5

For an existing project on Phase 1+1.5:

1. Run `act remote add <url>` (new subcommand). Creates the bare repo (option A), or uses existing remote (option B).
2. `git push -u <url> main` from inside `.act/.git`. Initial publish of all historical ops.
3. From this point on, the project is on Phase 2. Nothing about existing op files or fold semantics changes.

The host-repo's working tree is untouched. The `Act-Id:` trailer convention is untouched. The orchestrator's commands keep working — they just call the new write path under the hood.

Existing harvest mechanism (Phase 1+1.5) stays in the codebase. It is now the integration point for: (a) sandboxed workers with no network back to the remote, (b) failure recovery on workers that died mid-push, (c) onboarding a `.act/` from a fork or external contribution.

## Open questions for the design-review pass

1. **Bare repo location and naming.** `~/.local/share/act/<project-hash>/` vs. inside the project tree as a sibling to `.git`. The former survives `rm -rf <project>`; the latter is easier to back up alongside everything else.
2. **Cache TTL tuning.** 5s is a guess. Is there a class of workflow where 5s is way too long (e.g. interactive doctor) or way too short (e.g. CI sweep that runs 200 `act show` calls in a tight loop)?
3. **Remote auth for option B.** SSH keys vs. GitHub tokens vs. delegated auth. Probably "use whatever git is already configured to use" is the right answer — but worth surfacing.
4. **Worker discovery of "what's on the remote that I should know about" at startup.** Naive answer: fetch once at clone, fetch periodically thereafter. Sophisticated answer: subscribe to a notification stream (webhook, polling endpoint). Phase 2 should default to the naive answer; the sophisticated answer is Phase 3.
5. **Handling push retries in the failure-mode where the remote is unreachable for an extended time.** Local-write queues with eventual flush? Block the calling command? Fail loudly? Probably "fail loudly, with a `--offline` flag for explicit offline operation that queues for later."
6. **Garbage collection of orphan worker clones.** Worker died, sandbox got cleaned up before the clone was published or harvested. How does the orchestrator know there's nothing to recover vs. ops that need rescue? Probably a worker manifest with HLC ranges, but worth designing.

## Implementation rough-fanout

If this design survives review largely intact, the implementation tickets are roughly:

- A: bare-repo bootstrap subcommand (`act remote init`, `act remote add`).
- B: push-on-write integration in the actGitOps writer.
- C: fetch-on-read with TTL cache in the resolver.
- D: push-retry-with-fetch-merge helper for the contention loop.
- E: `git clone`-based bootstrap (replaces copy-on-dispatch for the remote-attached worker case).
- F: orchestrator updates — dispatch via clone, teardown without harvest (harvest still available as fallback).
- G: doctor updates — verify remote connectivity, surface drift, validate remote-vs-local op set.
- H: tests — concurrent-push retry, fetch-merge race, offline-mode behavior, multi-machine smoke test (if feasible).
- I: migration tooling — `act migrate-to-remote` for existing Phase 1+1.5 projects.

Six to nine tickets, depending on how aggressively A/B/C/D are split. Plan-review gate before tickets get filed.

## Cross-references

- Phase 1 design: `docs/coordination-plane-design.md` v2.1.
- Phase 1+1.5 implementation tickets: act-12dc23 (bootstrap-worker), act-9fadf0 (harvest), act-9e7078 (worker protocol), act-c8028f (tests), umbrella act-b77a80.
- HLC + `op_hash` for LWW: act-492e.
- `Act-Id:` trailer: act-c4c5.
- Nested-repo bootstrap: act-c1b4, act-9173.
