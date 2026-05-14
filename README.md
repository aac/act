# act — Agent Task Tracker

A single-binary task tracker designed for AI coding agents as the primary user. State lives as plain JSON files in `.act/` inside any git repo; concurrent agents on different machines merge their changes with `git pull --rebase`. No server, no database, no schema setup.

## Why this exists

Linear, Jira, and GitHub Issues are designed around human dashboards. When the worker is an agent — pulling work, claiming it atomically, committing results, filing follow-ups — every web form and human-facing surface is overhead. [Beads](https://github.com/gastownhall/beads) demonstrated the right shape for an agent-first tracker. `act` reimplements its load-bearing ideas — hash IDs, a ready queue, atomic claim, JSON-everywhere CLI, MCP-in-binary, git-as-distribution — on append-only JSON files that git merges naturally, instead of an embedded SQL database. The command surface is narrower, scoped for solo and small multi-agent use.

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
$ git commit -am "act show --full disables truncation (act-3c89)"
$ git push
```

The `(act-3c89)` marker in the commit message lets `act` correlate work commits with closed issues across sessions and machines. `act help workflow` documents the full canonical loop.

## How agents use this

`act` exposes its full surface as an [MCP](https://modelcontextprotocol.io) server (`act mcp`, stdio transport). MCP tools mirror the CLI one-to-one — `act_ready`, `act_create`, `act_claim`, `act_close`, and so on — so any MCP-aware agent (Claude Code, Cowork, custom SDK apps) can drive the loop without shelling out.

## Getting started

Requires Go 1.25+.

```sh
go install github.com/aac/act/cmd/act@latest
cd <any git repo>
act init                # creates .act/
act help                # tutorial; act help workflow for the canonical loop
```

To build from source instead: `git clone https://github.com/aac/act && cd act && go install ./cmd/act`.

## Status

`act` is at v0.1 and actively dogfooded on this repo's own backlog plus a few adjacent projects. Storage layout will see some churn before v1 as the coordination model settles.

Design docs: [`docs/spec-v2.md`](docs/spec-v2.md) (authoritative spec), [`docs/act-evaluation.md`](docs/act-evaluation.md) (live evaluation against real use).
