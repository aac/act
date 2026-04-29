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
- Reproducible state: any agent that pulls the repo can derive identical tracker state

## Anti-goals (explicitly not in scope)

- Human-facing dashboards or mobile apps; phone access happens through agents
- Project management features: time tracking, estimation, sprint planning, burndown
- Multi-tenant service or hosted offering
- A web UI of any kind in v1
- Multiple storage backends (op-log is the only backend)
- Any feature that requires runtime services beyond `git push/pull`

## Core data model

An **issue** has: `id` (hash, e.g. `act-a1b2`), `title`, `description`, `status` (open / in_progress / blocked / closed), `priority` (0–3), `type` (task / bug / epic / chore), `parent` (optional issue id), `deps` (list of issue ids with edge type: blocks / relates / supersedes), `assignee` (string, can name a human or an agent role), `acceptance_criteria` (structured list of strings), `created_at`, `closed_at`, `closed_reason`.

State is derived from an op log, not stored as mutable rows. See storage section.

## Storage architecture (op log, the load-bearing decision)

Every state change is an **append-only operation file**. Issues are not stored as monolithic JSON documents; they're stored as a sequence of ops that, when folded in commit-time order, produce the current state.

```
.act/
  ops/
    act-a1b2/
      2026-04-29T14:23:01Z-7f3a-create.json
      2026-04-29T14:24:15Z-9c2b-claim.json
      2026-04-29T15:02:44Z-3e1f-set-priority-0.json
  snapshots/
    act-a1b2.json          # optional, derived from ops, for fast reads
  index.db                  # SQLite, rebuilt-on-demand for fast queries
  config.json
```

**Why op-log:** two agents writing different ops to the same issue produce two new files. Git merges them trivially because new files never conflict textually. This gets us cell-level-merge-equivalent semantics without depending on Dolt. Two agents writing *logically* conflicting ops (e.g. both setting status to different values) produce two files; resolution is deterministic last-write-wins by timestamp with op-hash tiebreaker.

**Compaction:** periodic single-writer operation that snapshots an issue's op log to `.act/snapshots/<id>.json` and (optionally) prunes the ops directory. Keeps file count bounded for long-running issues. v1 compaction is mechanical only — no LLM summarization.

**Index:** SQLite file rebuilt on-demand from ops/snapshots for fast queries (`act ready`, `act list --status closed`). Treated as a derived cache, not source of truth. Never committed to git.

## Command surface (v1, target: 11 commands)

- `act init` — create `.act/` in the current git repo
- `act create "title" [-p N] [--parent ID] [--accept "criteria"] [--type T]`
- `act list [--status X] [--assignee Y] [--type T] [--json]`
- `act show <id> [--json]`
- `act update <id> [flags]` — `--status`, `--priority`, `--assignee`, `--description`, `--accept`, and `--claim` (atomic: assignee=$user + status=in_progress)
- `act close <id> [--reason TEXT]`
- `act dep add <child> <parent> [--type blocks|relates|supersedes]`
- `act dep rm <child> <parent>`
- `act ready [--under <id>] [--json]` — DAG roots with no open blockers, optionally scoped to a subtree
- `act doctor` — verifies git history vs tracker state; flags orphan closes (work committed referencing an issue but not closed)
- `act mcp` — start MCP server over stdio
- `act compact [--issue ID]` — snapshot the op log

JSON output is the default for any command an agent might call. Human-readable output is opt-in via the absence of `--json` and is not load-bearing.

## Distribution

- Single static Go binary; cross-compiled for macOS (intel + arm), Linux (intel + arm), Windows
- `brew install act` via homebrew tap; `curl -fsSL ... | sh` install script for everywhere else
- GitHub Releases for binaries with checksums
- The MCP server is `act mcp`, same binary, no separate Python package
- The Cowork plugin is a thin manifest that points at the binary and exposes the MCP tools

## Multi-writer semantics

The load-bearing scenario: two agents on different machines (or different branches) operate on the same repo concurrently.

- Op files are append-only, hash-named, never modified after creation. Filename includes ISO-8601 timestamp + a hash of the op payload.
- Two agents writing different ops to the same issue: each produces a new file in the same directory. `git pull --rebase` merges both with no conflict.
- Two agents writing logically conflicting ops to the same field: both files exist. Fold function applies last-write-wins by timestamp; ties broken by op-hash. Determinism is preserved.
- Atomic claim is implemented as a "claim" op + a check that no other claim op exists for the same issue at fold time. Conflicting claims resolve via the same LWW rule; the loser's session must `act show` to detect they didn't win.
- Compaction acquires a file lock and is single-writer. It runs locally and commits the snapshot.

## Git integration

- Stores `.act/` in the working tree; ops and snapshots are committed normally as part of the repo
- `act doctor` greps `git log` for `(act-XXXX)` patterns to detect orphan closes
- Optional: pre-commit hook to validate `act` state before commits land. Off by default.
- No special git refs (unlike Beads' Dolt refs). Plain files in plain git.

## MCP server

- `act mcp` exposes the CLI surface as MCP tools
- Tools mirror the commands one-to-one; no semantic translation
- Stdio transport; no network ports
- Cowork plugin manifest references the binary path; user installs `act` and adds the plugin

## Out-of-scope for v1 (explicit deferrals — be prepared to defend each)

- Hierarchical/dotted IDs — use parent edges instead
- Messaging / agent-to-agent comm — out
- Federation / multi-repo sync — out
- LLM-summarized compaction — v1 compaction is mechanical snapshot only
- Pubsub / live dispatch — agents pull, they don't subscribe
- Web UI / dashboard — out
- Multi-backend support — op-log is the only storage; SQLite is a derived cache
- Hooks framework — `act doctor` is one-shot, not event-driven

## Definition of done for v1

- Static binary builds for macOS (intel + arm), Linux (intel + arm), Windows
- All 11 commands implemented with `--json` for agent-relevant ones
- MCP server exposes equivalent tool surface
- Op-log → state derivation is deterministic and tested
- Concurrent-write integration test: two parallel processes writing to the same issue produce a coherent merged state via plain `git pull --rebase`
- Doctor detects orphan closes in a test repo
- Installs and runs end-to-end in fresh CC laptop, CC on the Web, and Cowork environments
- README explains the bootstrap workflow, op-log model, and the Beads divergence

## Review protocol for the build pipeline

The build runs as a chain of agent sessions, each producing an artifact reviewed by a downstream agent. Human is the seed and the final acceptor.

1. **Brief review** (this document) — adversarial review of architectural choices. Challenge: is op-log the right primitive vs alternatives (CRDT JSON, per-field files, just-use-Dolt)? Is the command surface load-bearing or vestigial? Are the deferrals correct or under-scoping? Is Go the right language? Does this anchor too hard on Beads' shape vs designing fresh? Output: numbered list of challenges to be incorporated or rebutted in a brief v2.
2. **Spec writing** — full spec from the locked brief: data model, edge cases, error handling, op-fold algorithm, conflict resolution rules, test plan, MCP tool surface details.
3. **Spec review** — hunt ambiguities. Output: numbered list of "two implementers would build different things from this" cases. Iterate until reviewer signs off.
4. **Decomposition** — ordered DAG of build issues, written into the same `_generated/projects/act/` tree as markdown files (since `act` doesn't exist yet to track its own build).
5. **Execution** — worker sessions pull next-ready issue, build, commit, mark done.

## Open questions for the brief reviewer

- Op-log vs CRDT-JSON-with-custom-git-merge-driver vs per-field-files: have we picked the right one, and is the trade-off articulated correctly?
- Go vs Bun-compiled TypeScript: is Go's discipline worth the language switch from Andrew's existing TS/Python stack, given that agents write the code?
- Is the 11-command surface the right cut, or is it missing something load-bearing (e.g. `act search`, `act stats`)?
- The `--claim` LWW resolution can produce a "you thought you claimed but didn't" outcome. Is that acceptable, or do we need a stronger primitive?
- Should `act` commit its own ops automatically (via a post-write git commit) or leave that to the agent? Trade-off: automatic commits make state durable but pollute git history.
- Does the brief anchor too hard on Beads? What would a fresh-eye design look like?

## Seed context

This brief is the output of an extended brainstorm reviewing Beads (gastownhall/beads) on its merits, identifying its load-bearing ideas (hash IDs, ready queue, atomic claim, JSON-everywhere CLI, MCP-in-binary, git-as-distribution) and divergence points (op-log instead of Dolt, narrower command surface, Go MCP instead of Python, no hierarchical IDs, no messaging). Adjacent reading: OpenAI Symphony (orchestration model, thin dispatcher + rich tracker), Anthropic Managed Agents (session/harness/sandbox runtime separation). The build pipeline will dogfood the multi-agent coordination model `act` is designed to support.
