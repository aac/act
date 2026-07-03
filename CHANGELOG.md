# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.1] - 2026-07-03

### Fixed
- `act mcp` now resolves the host repo **per tool call** from the client's
  workspace when one is supplied, instead of always using the server's process
  cwd. Under Codex — which launches the plugin MCP server with cwd = the plugin
  install dir and advertises no MCP `roots` capability — repo-relative tools
  (`act_init`, `act_create`, …) were operating in the plugin cache directory
  instead of the user's project. Codex's per-call workspace hint
  (`_meta."x-codex-turn-metadata".workspaces`) is now honored. Claude Code and
  the direct CLI are unaffected (they already resolved to the project).

## [0.4.0] - 2026-07-02

### Added
- `act blocks <id>` and `act blocked-by <id>` — read-only block-graph queries that
  print bare ids for shell pipes (`act show --json` remains the structured surface).
- `act update --unclaim <id>` — honestly release a claim (in_progress → open, clears
  the assignee) so a claimed-but-not-worked issue can be handed back and re-claimed.
- `act dep add <id> --external <ref>` — attach an external blocker through the same
  verb as internal deps (symmetric with `--blocked-by`).

### Changed
- External-blocker **adds** now go through `act dep add --external`. The old
  `act update --ext-add` is removed; `act update --ext-rm` is retained (it is the
  symmetric clear, matching internal `--dep-rm`). Breaking for anyone scripting
  `--ext-add`.

### Fixed
- `act.readCacheTTLSeconds` is now honored. The read-path freshness window was
  hardcoded to 5s and the config key was silently inert; it now tunes the window
  per-repo (unset falls back to the 5s default).
- `act.fetchTimeoutSeconds` is now honored. A hung `git fetch` had no wall-time
  bound; `FetchAndRebase` now aborts a fetch that exceeds the configured budget.
  Unset means unbounded (today's behavior); `act remote enable` writes the 10s
  default, so coordinated repos get the cap without regressing un-enabled ones.
- Close+reopen then re-claim no longer silently rejected: reopen (and unclaim) now
  clear the claim high-water mark so a reopened issue is claimable again.
- Concurrent pushes racing on git's object quarantine are now retried in
  `PushWithRetry` instead of surfacing a spurious failure.

## [0.3.1] - 2026-07-02

### Fixed
- `act mcp` now answers the JSON-RPC `initialize` handshake in any working directory,
  including one with no host git repo or `.act/`. Repo resolution is deferred to the
  tool calls that need tracker state (they return a `no_repo` envelope when it is
  absent), so MCP clients such as Codex that launch the server in a bare context
  (`./bin/act`, cwd `.`) complete registration instead of the server exiting before
  `initialize`.

### Changed
- Codex marketplace authentication policy `ON_FIRST_USE` → `ON_USE` (the real Codex
  enum value).

## [0.3.0] - 2026-07-02

### Removed
- `act install-skill` command and the in-binary skill `go:embed`. Skill delivery is
  now plugin-first: `/plugin install act@act` ships and auto-discovers the skill. The
  bare binary is CLI-only; a source install (`install.sh`) copies `skills/act/` from the
  checkout, and non-plugin users can copy it from a repo clone.

## [0.2.0] - 2026-06-28

This release reshapes act around a nested-repo storage model, adds an optional
multi-machine coordination plane, and ships the binary as an installable plugin for
both Claude Code and Codex. It folds in everything since 0.1.0.

### Added
- Composed CLI verbs: `act next` (ready + claim + show) and `act finish` (close + push),
  mirroring the `act_next` / `act_finish` MCP tools so either interface drives the whole
  loop in one call
- External dependencies: opaque cross-tracker block refs recorded on an issue; they gate
  both claim and close until satisfied (act-eef5, act-5e36)
- `act state import <dir>` / `act state export <dir>`: worktree-blind seeding and harvest
  of `.act/` state between repos (supersedes the bootstrap-worker/harvest pair)
- `act remote enable|disable|sync|add-upstream`: optional upstream coordination —
  push-on-write, fetch-rebase, post-receive hook, and a read-path TTL cache (Phase 2
  coordination plane)
- `act create --blocked-by <id>` / `--blocks <id>`: create an issue with a dependency
  edge in one atomic commit (act-c26a, act-ce77)
- `act dep add --blocks` / `--blocked-by` directional flag aliases (act-63a1)
- `act_file_blocker` MCP tool: file a blocks-edge between two existing issues (act-c26a)
- `act mine` and `act ready --mine`: self-scoped queries for issues claimed by the
  current node (act-c93b)
- `act show --full` (untruncated description/reason), `--include-ops` (renders ops in
  text mode), and surfacing of work commits attributed via the `Act-Id:` trailer
  (act-3c89, act-b891, act-9c8c)
- `act log` gains `--summary`, `--since`, `--by-issue`, and `--type` filters with a
  one-line-per-op timeline (act-56a0)
- `--branch <ref>` targets an explicit branch on write commands (act-5d6a)
- `--description-file` flag for `act create` and `act update` (act-6bbd)
- `act doctor` checks: orphan-close, time-travel, index-divergence, three-state
  reconciliation, plus `--fix-index` to rebuild a malformed index from the op-log, and
  `--no-code` / `--strict` modes (act-40ae, act-f2f93a, act-37f7)
- `act install-skill` (embeds the workflow skill in the binary) with `--target` and
  `--check`; `act help errors` and `act help workflow` topics; an `act help`
  agent-onboarding tutorial (act-b90e, act-acd9, act-aa8c, act-a854)
- `act init` auto-commits `.act/` and updates the host `.gitignore` by default (act-2c7d)
- `act ready` shows assignee and claimed_at columns

#### Packaging & release
- GoReleaser pipeline producing darwin/linux × amd64/arm64 binaries; darwin binaries are
  ad-hoc codesigned (fixes an arm64 SIGKILL) and smoke-verified on a macOS runner at
  release time
- Plugin packaging for Claude Code (`.claude-plugin/`) and Codex (`.codex-plugin/`),
  marketplace scaffolding, and a per-repo installer; conforms to the plugin-release-kit
  verifier
- `act init` ships an inert, toolchain-agnostic pre-close hook scaffold for projects to
  fill in

### Changed
- Phase 1 nested-repo layout: `.act/` is now its own git repository, gitignored from the
  host repo; work commits carry an `Act-Id:` commit-body trailer instead of an
  `(act-XXXX)` subject suffix (act-c4c5, act-9e8c)
- Short ids are 6–16 hex; unique-prefix resolution accepts any unambiguous prefix
  (act-f9a0, act-6fca)
- MCP dependency direction: `act_create` files blocks/blocked_by atomically, `act show
  --json` surfaces `blocked_by` / `blocks` arrays, and dependency display reads in the
  actual semantic direction (act-e26e and related)
- Claim is idempotent and works without an upstream remote (act-fdb2)
- `id_ambiguous` now exits 2 (usage) instead of 3 and includes `details.candidates[]`
  (act-8dcd)
- Auto-commit subject normalised to a canonical form across all write ops (act-d3a5)
- `act ready` returns only `status==open` issues (act-d79b)
- `act doctor` downgrades benign anomalies to warnings (exit 0); only error-severity
  findings block (act-37f7)
- Pre-close commit-marker check on `act close`, with a `--no-doctor` opt-out (act-f2ea)
- Index auto-adds missing columns on open (schema migration); hook timeout raised
  120s → 300s for the grown suite (act-492b5b)
- License changed to Apache 2.0 for sibling-tool consistency

### Fixed
- `act-repo` hooks (resolver + timeout) now fire correctly (act-8277)
- `act create` JSON output matches the spec shape; titles starting with `--` work via a
  `--` terminator (act-6181, act-6218)
- `act search` quotes FTS5 tokens so hyphens, periods, and colons in queries work
- `WriteOpsAndAutoCommit` rollback unstages only paths it staged (act-c22b)
- Hook stderr surfaces in close/create/update/reopen error envelopes (act-c83a)
- Push counters made race-safe with `sync/atomic` (act-e5fe)
- `claim_lost` exit code and `remote_unreachable` reachability reconciled with the spec
- Default priority is 2, matching the spec; unknown subcommand distinguished from
  "not implemented" (act-d9c7, act-c1be)
- `act close` validates `--reason` length (500-byte cap) up front (act-7ecd)
- Fold respects terminal `closed` status when applying a later claim (act-b7ad)
- Host pre-commit hook permits staged `.act/*` deletions; pull-rebase stderr suppressed
  when the local write succeeds (act-68f08b)
- Accidentally-committed worktree submodule refs removed; `.claude/` gitignored

### Deprecated
- `act bootstrap-worker` / `act harvest` superseded by `act state import` / `act state
  export`

### Removed
- `act redact` command and op type (act-8d1d)
- `act compact` user-visible surface (compaction is no longer an agent-facing command)

## [0.1.0] - 2026-05-01

Initial release.

### Added
- Project skeleton, Go module, and minimal CI (act-8411, act-9cad)
- On-disk layout and config schema (act-1396)
- Hybrid logical clock for op ordering (act-9cae)
- Canonical JSON serialization (act-b545)
- ID generation and nonce protocol (act-bd70)
- Shortest-unique-prefix display and resolution (act-6991)
- Op envelope schema and validation (act-ba09)
- Op type payloads and validation (act-3bbe)
- Op file naming and shard probe (act-6ec9)
- Op-fold algorithm core with LWW merge (act-9362)
- Per-op-type apply functions (act-c9f0)
- Per-field LWW exceptions and property tests (act-296e)
- Op-schema migration framework (act-5af9)
- Fold checkpoint (act-a1f6)
- Atomic claim protocol (act-9824)
- SQLite index schema and rebuild (act-912f)
- Auto-commit and push policy (act-5ca9)
- Hooks runtime contract (act-ce9f)
- `act init` command (act-b0b9)
- `act list` command (act-5bf7)
- `act log` command (act-5515)
- `act search` command (act-0a22)
- `act ready` command (act-e1d4)
- `act show` command (act-beca)
- `act create` command with interleaved flag/positional arg support (act-65e6)
- `act update` command (act-5651)
- `act close` command (act-bdc8)
- `act dep add` command (act-03f6)
- Compaction (act-a0ad)
- Doctor checks (act-40ae)
- Bootstrap importer (act-6eff)
- MCP server scaffold (act-380d)
- Composed MCP tools: `act_next`, `act_finish`, `act_block` (act-2f81)
- Concurrency and rebase contention tests (act-0c76)
- CI matrix and smoke tests (act-2e8d)
- Cross-platform release pipeline (act-64af)
- `act reopen` command (act-g002)
- `act delete` command (act-g009)
- `act redact` command (act-g008)
- Closer identity surfaced in `act show` (act-g001)
- Property tests and fuzzer (act-a64e)
- Golden tests for fold determinism (act-9b55)

### Fixed
- Error envelope shape unified across all commands per spec §error-envelope
- Priority 0 silently coerced to default in `act create`
- Canonical JSON `json.RawMessage` pass-through corrected

[Unreleased]: https://github.com/aac/act/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/aac/act/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/aac/act/releases/tag/v0.1.0
