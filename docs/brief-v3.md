# `act` — Agent Task Tracker

**One-liner:** A single-binary, git-resident task tracker designed so AI coding agents are the primary user. Humans interact with the work through the agents, not the tracker.

## Why this exists

Existing trackers are built for human teams. Linear, Jira, GitHub Issues all assume a human is reading dashboards and typing into web forms. When the primary worker is an agent — pulling work, claiming it atomically, committing results, filing follow-ups — the human-facing surface area is overhead. Beads (gastownhall/beads) demonstrates the right shape but has accreted features faster than any individual can hold in their head and depends on Dolt for storage, which is heavier than necessary for the solo-to-small-multi-agent scale we're targeting. `act` is a deliberately minimal reimplementation of Beads' best ideas, scoped for personal use, designed for clean evolution into multi-agent.

## North-star thesis

The agent is the primary user. Every primitive — output format, command shape, concurrency semantics, distribution mechanism — is decided by what makes agent operation faster and more reliable, not by what makes a human dashboard pretty. Humans interact with `act` only through agents, except in the rare cases where they need to sanity-check state directly via the CLI.

## Goals

- Solo-to-small-multi-agent personal use; one user, one repo at a time
- Lives in a git repo; no SaaS, no separate server, no auth infrastructure
- Static binary that runs in Claude Code (laptop), CC on the Web, and Cowork
- Single source of truth for "what work needs doing" across all agent sessions
- Multi-agent concurrency from day one (designed for it, not retrofitted)
- Reproducible state: for any sequence of supported queries, two folds of the same git tree return byte-identical JSON output. SQLite file bytes are explicitly not reproducible; query results are. CI enforces this.

## Anti-goals (explicitly not in scope)

- Human-facing dashboards or mobile apps; phone access happens through agents
- Project management features: time tracking, estimation, sprint planning, burndown
- Multi-tenant service or hosted offering
- A web UI of any kind in v1
- Multiple storage backends (op-log is the only backend)
- Any feature that requires runtime services beyond `git push/pull`

## Core data model

An **issue** has: `id` (full content hash; CLI displays the shortest unique prefix per session, git-style, with ties lengthened one hex at a time), `title` (mutable; renames are ordinary update ops), `description`, `status` (open / in_progress / blocked / closed), `priority` (0–3), `type` (task / bug / epic / chore), `parent` (optional issue id), `deps` (list of issue ids with edge type: blocks / relates / supersedes), `assignee` (string, can name a human or an agent role), `acceptance_criteria` (structured list of strings), `created_at`, `closed_at`, `closed_reason`.

The ID is the hash of `(create-op payload + random nonce)`, never of title. State is derived from an op log, not stored as mutable rows. See storage section.

The current taxonomy is the working baseline; the spec phase will produce a parallel fresh-eye model `(id, title, body, status, deps, claim)` and justify each retained Beads field on a concrete 3-task workflow. Fields that can't earn their keep there are dropped at spec time.

## Storage architecture (op log, the load-bearing decision)

Every state change is an **append-only operation file**. Issues are not stored as monolithic JSON documents; they're stored as a sequence of ops that, when folded in HLC order, produce the current state.

```
.act/
  ops/
    act-a1b2/
      2026-04/
        2026-04-29T14:23:01Z-7f3a-create.json
        2026-04-29T14:24:15Z-9c2b-claim.json
        2026-04-29T15:02:44Z-3e1f-set-priority-0.json
  snapshots/
    act-a1b2.json          # terminal or compacted snapshot, derived from ops
  fold-checkpoint.json      # keyed by git tree-hash of .act/ops/
  index.db                  # SQLite, rebuilt-on-demand for fast queries
  hooks/                    # post-create, post-close, post-claim executables
  config.json
```

Ops are sharded under `ops/<id>/<yyyy-mm>/` to bound per-directory entries. Budget: under 2k total files in `.act/` for a year-old project.

Every op carries `op_version`, `schema_version`, and `writer_version` in its payload. Fold dispatches per-version. On read, an op whose `writer_version` exceeds the reader's known max trips a structured "upgrade required" exit; `act version --check-repo` verifies compatibility ahead of time. Migrations are themselves ops written by `act migrate` — history is never rewritten in place. The op schema is a stability surface co-equal with the CLI.

**Why op-log:** two agents writing different ops to the same issue produce two new files. Git merges them trivially because new files never conflict textually. This gets us cell-level-merge-equivalent semantics without depending on Dolt. Two agents writing *logically* conflicting ops (e.g. both setting status to different values) produce two files; resolution is deterministic by HLC order with op-hash tiebreaker. Ops with implausible HLC deltas (>5 minutes from local) are refused at write time.

**Cold-start fold cost.** Folds reuse the SQLite cache when the git tree-hash of `.act/ops/` matches the persisted `fold-checkpoint.json`; otherwise only changed paths are refolded. Worst-case fold latency is budgeted and tested.

**Compaction.** Opportunistic, not user-invoked. Any writer holding the lock auto-compacts an issue past 50 ops or 30 days since its last snapshot. Closed issues collapse to a single terminal snapshot after 30 days. Deleted issues retain only a tombstone. Steady-state size budget: ~1KB per closed issue. v1 compaction is mechanical only — no LLM summarization. `act doctor --compact` is the manual escape hatch.

**Redact / delete.** A `redact` op is preserved as a tombstone; prior op payloads are NEVER mutated on disk (the immutability invariant holds). During fold, redact ops cause snapshots and query output to render the named fields as `"<redacted>"`. Reproducibility holds because redaction is deterministic from the op log. For true secret removal from git history, the documented escape hatch is `git-filter-repo`.

**Index.** SQLite file rebuilt on-demand from ops/snapshots for fast queries (`act ready`, `act list --status closed`, FTS5 for `act search`). Treated as a derived cache, not source of truth. Never committed to git.

## Command surface (v1)

- `act init` — create `.act/` in the current git repo
- `act create "title" [-p N] [--parent ID] [--accept "criteria"] [--type T]`
- `act list [--status X] [--assignee Y] [--type T] [--json]`
- `act show <id> [--json]`
- `act update <id> [flags]` — `--status`, `--priority`, `--assignee`, `--description`, `--accept`, `--dep-rm <id>`, and `--claim` (atomic: assignee=$user + status=in_progress)
- `act close <id> [--reason TEXT]`
- `act dep add <child> <parent> [--type blocks|relates|supersedes]`
- `act ready [--under <id>] [--json]` — DAG roots with no open blockers, optionally scoped to a subtree
- `act search <query> [--in title|desc|all]` — FTS5 over the SQLite index
- `act log <id>` — render the op stream for an issue
- `act doctor [--check <name>]` — battery of checks: `orphan-close`, `orphan-ops`, `dangling-deps`, `time-travel`, `cycle`, `unknown-op-version`, `index-divergence`, `index-schema`. `index-divergence` recomputes the index from ops/snapshots into a temporary SQLite and diffs row-by-row against `.act/index.db` (the recomputed db is the oracle). `index-schema` is a separate sub-check that forces a rebuild on schema mismatch rather than diffing. Default runs all.
- `act mcp` — start MCP server over stdio

`act compact` and standalone `act dep rm` are not user-facing commands. Compaction runs automatically on threshold; dep removal is a flag on `update`.

JSON output is the default for any command an agent might call. Human-readable output is opt-in via the absence of `--json` and is not load-bearing.

## Auto-commit and git history

Default behavior is auto-commit per op with an `act-op: <id> <op-type>` message prefix. Durability beats history aesthetics. Op-commits run with `git -c core.hooksPath=/dev/null` to bypass host repo pre-commit hooks (op commits touch only `.act/ops/**` and are validated by `act doctor`); `--verify` opts back into host hooks. `--no-commit` is available for batching agents. `act` does NOT push by default — auto-commit yes, auto-push no. `--push` is opt-in per command. `--isolated` shares the same code path: commit locally, no network. Before push, `act` collapses contiguous `act-op:` commits into a single squashed commit so the visible history isn't drowned in tracker noise.

## Distribution

- Single static binary; cross-compiled for macOS (intel + arm), Linux (intel + arm), Windows
- Default language is Go, contingent on a 1-day Go-vs-Bun spike measuring the cross-compile matrix and op-fold determinism. Default justified by static-binary maturity for the 5-target matrix; spike must run before the language is locked.
- `brew install act` via homebrew tap; `curl -fsSL ... | sh` install script for everywhere else
- GitHub Releases for binaries with checksums
- The MCP server is `act mcp`, same binary, no separate Python package
- The Cowork plugin is a thin manifest that points at the binary and pins a specific binary version, so writer/reader versions can't drift across the fleet

## Multi-writer semantics

The load-bearing scenario: two agents on different machines (or different branches) operate on the same repo concurrently.

- Op files are append-only, hash-named, never modified after creation. Filename includes ISO-8601 timestamp + a hash of the op payload; payload carries an HLC.
- Two agents writing different ops to the same issue: each produces a new file in the same monthly shard. `git pull --rebase` merges both with no conflict.
- Two agents writing *logically* conflicting ops to the same field: both files exist. Fold applies HLC order; ties broken by op-hash. Determinism is preserved. Ops carry a `node_id = sha256(machine-id || git-config user.email)[0:8]`, generated at `act init` and stored in `.act/config.json`, included in payload and filename hash so identical `(physical, logical)` from two machines cannot collide. The HLC plausibility reference is `max(local_wall, last_seen_hlc_in_repo)`; ops with drift >5 minutes from that reference are refused at write time, so fresh containers catch up to repo time on first read.
- `--claim` is blocking-and-verifying. The protocol is: (1) write the claim op, (2) `git pull --rebase` from the configured remote, (3) re-fold the issue, (4) report win/loss, (5) push only on win unless `--push` is also passed. On loss, exit non-zero with structured `{"claimed": false, "winner": "..."}`. `--wait` polls to stability; `--isolated` opts out of remote fetch for offline use.
- Compaction acquires a file lock and is single-writer. It runs locally and commits the snapshot.

## Git integration

- Stores `.act/` in the working tree; ops and snapshots are committed normally as part of the repo
- `act doctor` scans both commit messages and the diff of `.act/ops/**` for close-ops; any commit touching an issue's ops directory is evidence. Snapshots carry a `closed_by_commit` reverse index for symmetric verification, so squash and rebase don't blind the check.
- Optional: pre-commit hook to validate `act` state before commits land. Off by default.
- No special git refs (unlike Beads' Dolt refs). Plain files in plain git.

## Hooks

`.act/hooks/{post-create,post-close,post-claim}` are plain executables run synchronously after the corresponding op is committed. Hooks receive the op JSON on stdin and `ACT_OP_ID`, `ACT_OP_TYPE`, `ACT_ISSUE_ID`, `ACT_HOOK_PHASE` env vars. Default 5s timeout (SIGTERM then SIGKILL); non-zero exit fails the op and rolls back the staged op file before commit. Hooks NEVER run during fold, replay, import, or clone — only on the writer that originally produced the op. That's the entire framework in v1. Pubsub and event-bus stay deferred.

## MCP server

- `act mcp` exposes the CLI surface as MCP tools
- 1:1 tools for each command, plus three composed convenience tools: `act_next` (ready + claim + show), `act_finish` (close + commit-marker), `act_block` (status=blocked + add-dep). Tool descriptions mark the composed tools as the recommended path. On claim loss, `act_next` performs bounded retry with exponential backoff (max 3 tries, 100ms → 400ms → 1.6s, jittered), refolding the ready queue and excluding just-lost issues each attempt; on exhaustion it returns `{"claimed": false, "candidates": [...]}` so the caller picks the next move. No implicit retry beyond the bound.
- Stdio transport; no network ports
- Cowork plugin manifest references the binary path; user installs `act` and adds the plugin

## Bootstrap

A 50-line shell script writes `_generated/projects/act/issues.jsonl` — the simplest possible op-log. Each line is one op object using the v0.1 envelope: `{op_version, schema_version, op_type, issue_id, payload, hlc, node_id}`. v0.1 of `act` ships an importer that validates each line, replays it as if locally generated (assigning fresh HLCs and filenames), and emits an id-mapping file. The op-log primitive is dogfooded from line one and the project tracks itself before the binary is finished.

## Out-of-scope for v1 (explicit deferrals — be prepared to defend each)

- Hierarchical/dotted IDs — use parent edges instead
- Messaging / agent-to-agent comm — out
- Federation / multi-repo sync — out
- LLM-summarized compaction — v1 compaction is mechanical snapshot only
- Pubsub / live dispatch — agents pull, they don't subscribe; file hooks cover the synchronous case
- Web UI / dashboard — out
- Multi-backend support — op-log is the only storage; SQLite is a derived cache

## Testing strategy

- Property tests asserting op-fold permutation invariance for commutative op pairs
- Golden tests per op type: input op + prior state → expected post-state
- Fuzzer that generates random op sequences and asserts deterministic fold output
- MCP end-to-end via a fake stdio client driving the composed tools
- Git rebase contention tests: two worktrees racing claims, asserting exactly one winner and structured loss output

## Definition of done for v1

- Static binary builds for macOS (intel + arm), Linux (intel + arm), Windows
- All v1 commands implemented with `--json` for agent-relevant ones
- MCP server exposes the 1:1 + composed tool surface
- Op-log → state derivation is deterministic and tested by the strategy above
- Concurrent-write integration test: two parallel processes writing to the same issue produce a coherent merged state via plain `git pull --rebase`
- Doctor's full check battery passes on a seeded test repo
- Three CI jobs containerize each target environment (CC laptop, CC on the Web, Cowork), exercise an end-to-end install + workflow, and report PASS/FAIL. Agent runs them. Human signs off only on the final tag.
- README explains the bootstrap workflow, op-log model, and the Beads divergence

## Review protocol for the build pipeline

The build runs as a chain of agent sessions, each producing an artifact reviewed by a downstream agent. Human is the seed and the final acceptor.

1. **Brief review** (this document) — adversarial review of architectural choices. Challenge: is op-log the right primitive vs alternatives (CRDT JSON, per-field files, just-use-Dolt)? Is the command surface load-bearing or vestigial? Are the deferrals correct or under-scoping? Is Go the right language? Does this anchor too hard on Beads' shape vs designing fresh? Output: numbered list of challenges to be incorporated or rebutted in a brief v2.
2. **Spec writing** — full spec from the locked brief: data model (with the fresh-eye comparison artifact), edge cases, error handling, op-fold algorithm, HLC and conflict resolution rules, op schema versioning, test plan, MCP tool surface details.
3. **Spec review** — hunt ambiguities. Output: numbered list of "two implementers would build different things from this" cases. Iterate until reviewer signs off.
4. **Decomposition** — ordered DAG of build issues, written into the same `_generated/projects/act/` tree as the seed JSONL (since `act` doesn't exist yet to track its own build, until v0.1 imports it).
5. **Execution** — worker sessions pull next-ready issue, build, commit, mark done.

## Open questions for the brief reviewer

- Op-log vs CRDT-JSON-with-custom-git-merge-driver vs per-field-files: have we picked the right one, and is the trade-off articulated correctly?
- Go vs Bun-compiled TypeScript: the 1-day spike is committed; what's the bar for flipping the default?
- Is the v1 surface the right cut, or is it missing something load-bearing beyond `search` and `log`?
- Is blocking-and-verifying claim the right strength, or do we want a stronger primitive (e.g. ref-based lock) at the cost of git plumbing?
- Does the brief still anchor too hard on Beads after the spec-phase fresh-eye comparison commitment?

## Seed context

This brief is the output of an extended brainstorm reviewing Beads (gastownhall/beads) on its merits, identifying its load-bearing ideas (hash IDs, ready queue, atomic claim, JSON-everywhere CLI, MCP-in-binary, git-as-distribution) and divergence points (op-log instead of Dolt, narrower command surface, Go MCP instead of Python, no hierarchical IDs, no messaging). Adjacent reading: OpenAI Symphony (orchestration model, thin dispatcher + rich tracker), Anthropic Managed Agents (session/harness/sandbox runtime separation). The build pipeline will dogfood the multi-agent coordination model `act` is designed to support.
