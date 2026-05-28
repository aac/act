# act — Agent Task Tracker

A single-binary task tracker designed for AI coding agents as the primary user. State lives as append-only JSON op files inside a *nested* git repo at `.act/` — its own `.git/` directory, gitignored from the surrounding code repo. Concurrent agents merge their writes with plain git semantics. No server, no database, no schema setup.

## Why this exists

Linear, Jira, and GitHub Issues are designed around human dashboards. When the worker is an agent — pulling work, claiming it atomically, committing results, filing follow-ups — every web form and human-facing surface is overhead. [Beads](https://github.com/gastownhall/beads) demonstrated the right shape for an agent-first tracker. `act` reimplements its load-bearing ideas — hash IDs, a ready queue, atomic claim, JSON-everywhere CLI, MCP-in-binary, git-as-distribution — on append-only JSON files that git merges naturally, instead of an embedded SQL database. The command surface is narrower, scoped for solo and small multi-agent use.

## Storage layout

`.act/` is a nested git repository: it has its own `.git/` and its own history of op commits (claims, closes, dep edges, etc.). The host code repo gitignores `.act/`, so external contributors who clone the public repo see exactly the codebase — no tracker state, no `act-op:` commits in the host `git log`. The only act-shaped artifact in the host repo's history is an `Act-Id: act-XXXXXX` trailer in the body of work commits authored by agents; trailers are invisible to conventional-commit linters and survive squash-merge cleanly. `act doctor` cross-references the host's marker trailers against the nested op-log to flag drift.

This layout (Phase 1) is what `act init` produces today. Migrating an older single-repo `.act/` to the nested layout is a one-shot `act migrate-to-nested` — see [`docs/migration-runbook.md`](docs/migration-runbook.md).

The Phase 1.5 worker-isolation pivot copies `.act/` into each dispatched worker and harvests new ops back at orchestrator teardown; this is the current default for `/orchestrate`-style fanout. Phase 2 (push-attached workers) replaces the copy-and-harvest cycle with each worker pushing ops directly to the orchestrator's `.act/.git` over the local filesystem or SSH, using `.act/.git` itself as the canonical upstream. Operators who want cross-machine or sandboxed-worker coordination enable it per the design in [`docs/coordination-plane-design.md`](docs/coordination-plane-design.md) (Phase 1 / 1.5) and [`docs/coordination-plane-phase2-design.md`](docs/coordination-plane-phase2-design.md) (Phase 2).

## What a session looks like

```sh
$ act ready
act-3c89 2 cli: act show --full to disable description and reason truncation
act-7ecd 2 cli: act close --reason validates length upfront, not on rejection
act-4b45 2 cli: act ready shows assignee and claimed_at columns
...

$ act update --claim act-3c89    # atomic; concurrent claimers resolve last-write-wins
# ...write the code, run the tests...
$ act close act-3c89 --reason "added --full flag; tests cover both truncation paths"
                                 # writes + commits the close op in the nested .act/ repo
$ git commit -am "act show --full disables truncation" \
             -m "Act-Id: act-3c89"
$ git push
```

The `Act-Id: act-3c89` trailer in the commit body lets `act doctor` correlate work commits with closed issues across sessions and machines. `act help workflow` documents the full canonical loop.

## How agents use this

`act` exposes its full surface as an [MCP](https://modelcontextprotocol.io) server (`act mcp`, stdio transport). MCP tools mirror the CLI one-to-one — `act_ready`, `act_create`, `act_claim`, `act_close`, and so on — so any MCP-aware agent (Claude Code, Cowork, custom SDK apps) can drive the loop without shelling out.

## Getting started

Requires Go 1.25+.

```sh
go install github.com/aac/act/cmd/act@latest
cd <any git repo>
act init                # creates nested .act/ (its own .git/), adds .act/ to host .gitignore
act help                # tutorial; act help workflow for the canonical loop
```

To build from source instead: `git clone https://github.com/aac/act && cd act && go install ./cmd/act`.

## Skill installation (Claude Code)

The canonical workflow doc for any project using `act` is the Claude Code skill at `~/.claude/skills/act/SKILL.md` (plus its `references/` companions). The skill is bundled into the `act` binary itself via `go:embed` — there is no separate download. Install or refresh it with:

```sh
act install-skill              # writes to ~/.claude/skills/act/
act install-skill --force      # overwrite local edits to canonical files
act install-skill --dest PATH  # alternate destination
```

`install-skill` is idempotent: files already matching the embedded copy are skipped; files that diverge are reported and left untouched unless `--force` is passed. Re-run after every `act` upgrade to keep agents on the current workflow.

## Status

`act` is at v0.1 and actively dogfooded on this repo's own backlog plus a few adjacent projects. The Phase 1 nested-repo layout is as-built; Phase 1.5 worker isolation is shipping; Phase 2 push-attached coordination is in implementation.

Design docs: [`docs/spec-v2.md`](docs/spec-v2.md) (authoritative spec), [`docs/coordination-plane-design.md`](docs/coordination-plane-design.md) (Phase 1 / 1.5 nested-repo design), [`docs/coordination-plane-phase2-design.md`](docs/coordination-plane-phase2-design.md) (Phase 2 push-attached workers), [`docs/migration-runbook.md`](docs/migration-runbook.md) (one-shot legacy → nested migration), [`docs/act-evaluation.md`](docs/act-evaluation.md) (live evaluation against real use).

## License

MIT — see [`LICENSE`](LICENSE).
