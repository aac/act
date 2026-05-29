# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-05-15

### Added
- `act create --blocks <id>`: symmetric counterpart to `--blocked-by`; files a blocks-edge in the inverse direction alongside the create op
- `act create --blocked-by <id>`: creates a new issue that is blocked by an existing one in a single atomic commit (act-c26a)
- `act_file_blocker` MCP tool: adds a blocks-edge between two existing issues from an MCP client
- `act dep add --blocks` / `--blocked-by` directional flag aliases (act-63a1)
- `act show` surfaces work commits attributed via `Act-Id:` body trailer (act-9c8c)
- `act mine` and `act ready --mine`: self-scoped queries showing only issues claimed by the current node
- `act init` auto-commits `.act/` and updates `.gitignore` by default (act-2c7d)
- `act show` text mode renders description and commit_marker fields (act-10f7)
- `per_session` bundle strategy: collapses claim + work + close into fewer commits (act-728d)
- `--description-file` flag for `act create` and `act update` (act-6bbd)
- `act help errors` topic documenting the error-envelope contract (act-acd9)
- `act help workflow` documents commit_marker invariants (act-aa8c)
- MCP composed tools: `act_next`, `act_finish`, `act_block` (act-2f81) — backported from v0.1.0 stream
- Pre-close hook gates for gofmt, vet, and test
- `act doctor`: orphan-close detection, time-travel checks, index-divergence checks (act-40ae)
- `act_next` and `act show` surface `(act-XXXX)` commit_marker in output (act-5467)
- Bootstrap dogfood loop: act now tracks its own v0.2 backlog

### Changed
- Claim is idempotent and works without an upstream remote (act-fdb2)
- `id_ambiguous` error now exits 2 (usage) instead of 3; includes `details.candidates[]` (act-8dcd)
- Unique-prefix lookup accepts any non-empty hex prefix (removed `MinShortHexLen=4` floor from input resolution; act-6fca)
- Auto-commit subject canonical form normalised across all write ops (act-d3a5)
- `act ready` returns only `status==open` issues (act-d79b)
- Phase 1 nested-repo layout: `.act/` is now its own git repository, gitignored from the host repo; work commits carry `Act-Id:` trailers instead of `(act-XXXX)` subject suffixes

### Fixed
- `act-repo` hooks (resolver + timeout) now fire correctly (act-8277)
- `WriteOpsAndAutoCommit` rollback unstages only staged paths (act-c22b)
- Hook stderr surfaces in close/create/update/reopen error envelopes (act-c83a)
- `act create` titles starting with `--` work via `--` terminator (act-6218)
- Worktree submodule refs accidentally committed; `.claude/` added to `.gitignore`
- `act ready` no longer returns closed issues (act-d79b)

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
