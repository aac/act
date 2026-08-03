# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- **`act init` no longer touches the host repo unasked (act-66f987).** It used
  to commit `.gitignore` + a generated `CONTRIBUTING.md` stanza to the host
  repo on every init, and to write that stanza automatically for any repo with
  a github/gitlab/bitbucket remote. Across the 2026-07-28/29 bootstrap of 17
  repos that produced an unrequested commit in every host repo with host-side
  changes; 13 needed hand-reverting.
  - The host commit is now opt-in: `act init --commit-host`. By default init
    writes `.gitignore` and the pre-commit hook into the working tree, names
    them in `host_files_uncommitted`, and leaves the commit to the operator.
  - The CONTRIBUTING stanza is opt-in: `act init --contributing`. A
    public-looking remote now only sets `contributing_suggested` and prints
    the offer.
  - An already-active pre-close gate (`.act/hooks/close`) is left alone and no
    `close.sample` is dropped beside it, including under `--force`.
  - `cli.RunInit` takes an `InitOptions` struct instead of four positional
    parameters. The MCP `act_init` tool is unchanged and deliberately does not
    expose either opt-in.

### Added
- **A genuinely non-mutating read mode: `--no-fetch` / `ACT_NO_FETCH=1`
  (act-3803ac).** `ready`, `list`, `show`, `log` and `search` refresh the
  store before reading — a `git pull --rebase` inside the nested `.act/.git`
  that, when HEAD moves, deletes the fold checkpoint and `index.db`. A sweep
  across N stores was therefore N rebases into repos an agent fleet may be
  writing to concurrently, and every reader had to reimplement a busy-window
  discipline or accept the hazard. Under `--no-fetch` the read returns before
  any git command runs: HEAD, `FETCH_HEAD`, the fold checkpoint and `index.db`
  all come out unchanged.
- **Read commands report the refresh outcome instead of discarding it
  (act-3803ac).** The five read paths called `_, _ = MaybeRefresh(...)`, so a
  failed refresh — unreachable bare host, no network — looked exactly like a
  successful one and the command served on-disk state with exit 0. `--json`
  output now carries a `refresh` object (`reason`, `fetched`, `invalidated`,
  `age_seconds`, and `error` when the refresh failed), and a failed refresh
  prints a WARNING to stderr in both human and `--json` mode. The command
  still exits 0: stale-but-readable is a usable answer as long as the caller
  is told. `age_seconds` is the age of `FETCH_HEAD`, which saves aggregators
  from stat'ing it themselves.
- **`act ready` rejects `--no-fetch` with `--fresh`/`--no-cache`** (exit 2)
  rather than silently picking one; they ask for opposite things.

### Fixed
- **`act ready --limit 0` returns every ready issue, and a capped ready set
  now says so.** `--limit 0` meant "no limit" on `act list` and "fall back to
  the default 50" on `act ready`, so on one real store `act ready --json
  --limit 0` answered 50 while `--limit 500` answered 99 — the two
  subcommands disagreeing on the same flag, with no way to ask ready for a
  complete answer.
  - `act ready --json` now carries `total` (the pre-limit ready count) and
    `truncated`, matching `act list` field-for-field. It previously carried
    only `{ready, count}`, so a caller could not tell a project with exactly
    50 ready issues from one with 500 — every cross-store count silently
    under-reported, with exit 0.
  - A capped ready set prints a WARNING to **stderr** in both human and
    `--json` mode, naming how many issues were hidden and pointing at
    `--limit 0`. stderr because `act ready --json | jq` owns stdout.
  - The 50-row default moved from `RunReady` to its callers (the `act ready`
    flag default, `act_next`'s frontier fetch, the `act_ready` MCP tool),
    which is what makes `--limit 0` expressible at all. `ReadyOptions.Limit`
    now means "no limit" at `<=0`, the same as `ListOptions.Limit`.

### Added
- **`claimed_at` on `act list --json`, `created_at` on `act ready --json`.**
  Claim age was reachable only through `act show`, so an aggregator sweeping
  many stores paid one subprocess per in-progress id — unbounded fan-out
  keyed on exactly the projects with the most stale claims. Both fields are
  additive and omitted when empty; nothing was removed or renamed.
- **`act update <id> --description-append-file <path|->`.** The file twin of
  `--description-append`, for a session record too long or too quote-hostile
  for one shell argument; `-` reads stdin. Mutually exclusive with
  `--description`, `--description-file` and `--description-append`.

### Changed
- **`short_id` is emitted only when it differs from `id`.** Ids are
  generated at the shortest-unique-prefix floor, so unless an id was
  extended to break a collision the two fields carried byte-identical
  strings — duplicated on every row of every listing. Applies to every
  payload that carried both (`act list`, `act ready`, `act search`,
  `act show`, `act close`, `act delete`, `act reopen`, and their MCP
  equivalents), CLI `--json` and MCP alike, so the field means one thing
  everywhere.
  - **Consumers must read an absent `short_id` as `short_id == id`** —
    "short_id, else id" — not as a missing field. It reappears whenever it
    carries information, i.e. for an extended id.
  - Human output is unchanged: `act list` and `act ready` still print the
    short handle as the row id.
  - Measured on a live tracker (17 open issues): `act_list` 3,991 → 3,583 B,
    `act_ready` 2,661 → 2,301 B — 24 B per row.
- **The MCP server advertises eight tools instead of sixteen.** `tools/list`
  now carries only what the work loop runs: `act_next`, `act_finish`,
  `act_block`, `act_file_blocker`, `act_list`, `act_show`, `act_create`,
  `act_update`. Combined with the schema trim below, the advertised surface
  goes 14,320 B → 6,281 B (-56%).
  - The setup and diagnostic verbs — `act init`, `act version`, `act doctor`,
    `act log`, `act search`, `act ready`, `act close`, `act dep add` — are
    **CLI-only**, each reachable as a command of the same name. They also
    still dispatch over MCP if named directly, so a client holding a cached
    older tool list keeps working.
- **MCP tool schemas are ~34% smaller (14,320 B → 9,420 B across the 16
  tools).** The schema text is re-read on every turn of every session that
  wires the server, so it is a recurring cost rather than a one-off.
  - `read_only` no longer appears on any tool schema. It was a per-call
    advisory nothing enforced (its own description deferred to the
    server-level `--read-only` flag) and it cost ~2 KB — 14% of the whole
    surface. Read-only enforcement is unchanged: `act mcp --read-only`
    still refuses every write tool.
  - `no_commit` and `isolated` are no longer advertised on the four tools
    that carried them. An agent driving the tracker over MCP has no reason
    to skip the auto-commit that makes a write durable and shareable.
    `push` is still advertised.
  - Cross-tool explanatory prose (the `act dep add` direction worked
    example, the accept-vs-accept_add contrast) moved into the act skill,
    which an agent reads once, out of the schemas it re-reads every turn.
  - No parameter was renamed, retyped, or changed in required-ness, and
    tool schemas leave `additionalProperties` unconstrained — a client
    holding a cached older schema that still sends `read_only`,
    `no_commit`, or `isolated` behaves exactly as before.
- **BREAKING: `act list` now lists the working set — closed issues are
  excluded from the default listing.** A tracker accumulates closed work
  indefinitely; in the repo that surfaced this, 264 of 268 issues were
  closed, so a default listing was almost entirely finished work and the
  200-row cap spent its whole budget on it — open work, the only thing
  anyone lists a tracker to find, fell off the end. `act list` (and the
  `act_list` MCP tool) now default to `open,in_progress,blocked`, matching
  what `docs/spec.md` has always specified ("Default: all non-closed") and
  what `gh issue list` does.
  - Closed issues stay one flag away: `act list --status closed` for
    finished work, `act list --all` for every status. `--all` and
    `--status` together are rejected with exit 2 rather than one silently
    winning.
  - When closed rows appear alongside live work (`--all`, or an explicit
    `--status open,closed`), they sort **after** every non-closed row.
    The grouping is applied ahead of `--sort` and is not overridable —
    the default priority-asc sort put a closed p0 above an open p3, which
    is exactly the burial this change exists to prevent. `--sort` still
    orders rows within each group.
  - Migration: anything that relied on `act list` returning closed rows
    needs `--all` or `--status closed`. Scripts that were filtering the
    default listing down to open work (`act list | grep open`) keep
    working and get more accurate — that filter was the incident.

### Added
- `act list --all` and the matching `all` parameter on the `act_list` MCP
  tool.
- `act update <id> --description-append "<note>"` appends to an issue's
  existing description instead of replacing it, resolving the current body
  server-side. `act_update` advertises the matching `description_append`,
  and its `description` parameter now says it REPLACES so the pair is
  distinguishable. Before this, the only append path was a read-modify-write
  through `--description-file` — which is why agents reached for `act log`
  to leave notes.

### Fixed
- A capped `act list` says so. The 200-row cap silently truncated, so a
  caller piping the listing into a filter got a confident under-count; one
  such count closed a p1 ticket on a wrong number. A capped listing now
  prints a WARNING to **stderr** (not stdout — the incident was a pipe,
  which swallows a stdout trailer) naming how many issues were hidden, and
  `--limit 0` means "no limit" instead of exiting 2. JSON output gains
  `total` (pre-limit match count) and `truncated`, both always present, so
  a consumer tests one boolean instead of inferring truncation from
  `count == limit` — which is wrong exactly when the match count equals the
  limit.
- Read-only verbs (`log`, `show`, `list`, `ready`, `mine`, `search`) reject
  an unexpected positional with exit 2 and a hint naming the command the
  caller wanted, instead of exiting 0 having dropped it. `act log <id>
  "message"` silently swallowed the message: four annotations across three
  trackers were lost that way with no error anywhere. Write verbs are
  deliberately unchanged.
- `act`'s internal git operations on the nested `.act/` repo no longer spawn
  detached background maintenance processes. Auto-maintenance now runs in the
  foreground (`--no-detach`), so no unwaited `git maintenance` child outlives
  the `act` command that triggered it — previously a source of test flakes and
  of stray processes contending with the store. The cost, measured: below the
  gc threshold, foreground maintenance adds ~0.01s per triggering operation;
  when gc actually fires (roughly once per several thousand operations), the
  triggering command blocks ~2s instead of that work happening detached.
- Concurrent pushes no longer fail hard on a receive-pack object-migration
  race. `PushWithRetry` already treated three signatures of git's
  object-quarantine race as retryable contention; a fourth —
  `unable to migrate objects to permanent storage`, which receive-pack
  reports when migrating a pusher's *own* quarantined objects into the
  permanent store loses the race — fell through to the "other push failure"
  path and surfaced a transient race as a hard error. Retrying is safe
  because the rejection precedes the ref update, so the remote keeps its
  prior tip. Measured on a 10-core mac mini: 12 failures in 240
  concurrent-push runs under load before, 0 in 240 after, and 0 in 20 runs
  on an idle box either way (which is why it only ever appeared as a
  once-in-a-while CI flake).

## [0.4.2] - 2026-07-22

### Fixed
- `act update --status open` now honestly releases a claim instead of silently
  no-opping. A stale `in_progress` claim is released **and** the claim
  high-water mark is cleared, so the issue re-enters `ready` and can be
  re-claimed. `closed → open` via `--status` is now rejected with a pointer to
  `act reopen` rather than silently doing nothing.
- Git stale-lock wedges are detected and reported clearly. A lingering
  `index.lock` / `HEAD.lock` left by a crashed process made write commands fail
  opaquely; gitops now recognizes git's stale-lock stderr and returns a typed
  `stale_git_lock` envelope (lock path + recovery steps) from every write
  command, and a new read-only `act doctor` check flags a lingering lock with
  the remedy. The lock is never auto-removed — even under `--fix` — because lock
  liveness isn't portably provable.
- Fold tolerates unknown `op_type`s on the read path. An `.act/ops` tree
  containing an op written by a newer `act` failed the entire fold-for-rebuild
  at the parse layer, bricking `act list`, state export, and
  `doctor --fix-index`. Unknown types now flow through and are skipped (matching
  the apply path's existing tolerance) while every other corruption still
  aborts — forward-compatibility for a tracker touched by a newer binary.

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

[Unreleased]: https://github.com/aac/act/compare/v0.4.2...HEAD
[0.4.2]: https://github.com/aac/act/compare/v0.4.1...v0.4.2
[0.2.0]: https://github.com/aac/act/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/aac/act/releases/tag/v0.1.0
