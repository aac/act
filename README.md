# act — agent task tracker

A single-binary task tracker designed for AI coding agents as the primary user. State
lives as append-only JSON op files inside a *nested* git repo at `.act/` — its own
`.git/` directory, gitignored from the surrounding code repo. Concurrent agents merge
their writes with plain git semantics. No server, no database, no schema setup.

## Why this exists

Linear, Jira, and GitHub Issues are built around human dashboards. When the worker is an
agent — pulling work, claiming it atomically, committing results, filing follow-ups —
every web form and human-facing surface is overhead. `act` is the agent-shaped
alternative: hash IDs, a ready queue, atomic claim, JSON-everywhere CLI, an MCP server in
the binary, and git as the distribution layer, on append-only JSON files that git merges
naturally. (The agent-first tracker shape was demonstrated by
[Beads](https://github.com/gastownhall/beads); act reimplements the load-bearing ideas on
git-merged JSON instead of an embedded database, with a narrower surface scoped for solo
and small multi-agent use.)

Because `.act/` is a nested repo with its own history, the host code repo gitignores it —
so anyone who clones the code sees exactly the codebase, no tracker state in the host
`git log`. The only act artifact in host history is an `Act-Id: act-XXXXXX` trailer in the
body of agent-authored work commits; trailers are invisible to conventional-commit
linters and survive squash-merge. `act doctor` cross-references those trailers against the
op-log to flag work that was committed but never closed.

## Quick start

You don't run act as a daily driver yourself; you install the plugin and your agent drives
it. In **Claude Code**:

```text
/plugin marketplace add aac/act
/plugin install act@act
```

The plugin bundles the `act` binary, the workflow skill, and the MCP server. Then, in any
git repo:

```sh
act init      # creates the nested .act/ (its own .git/) and gitignores it from the host
act help      # tutorial; `act help workflow` for the canonical claim/work/close loop
```

## What a session looks like

```sh
$ act ready
act-3c89 2 cli: act show --full to disable description and reason truncation
act-7ecd 2 cli: act close --reason validates length upfront
act-4b45 2 cli: act ready shows assignee and claimed_at columns

$ act update --claim act-3c89    # atomic; concurrent claimers resolve last-write-wins
# ...write the code, run the tests...
$ act close act-3c89 --reason "added --full flag; tests cover both truncation paths"
$ git commit -am "act show --full disables truncation" -m "Act-Id: act-3c89"
$ git push
```

Two composed verbs bundle the common loop steps so a session needs fewer calls:

```sh
$ act next      # ready + claim + show: claims the top unblocked issue and prints it
# ...write the code, run the tests...
$ act finish act-3c89 --reason "added --full flag; tests cover both truncation paths"
                # close: closes the issue and pushes the close op to the .act/ tracker
                # remote (skips the push with no origin). It does NOT push your host
                # work commit — you still `git push` your code yourself.
```

The `Act-Id: act-3c89` trailer lets `act doctor` correlate work commits with closed issues
across sessions and machines.

## Installing

Installing the plugin is the canonical path — it bundles the binary, the skill, and the
MCP server. act is built for **Claude Code** and **Codex**, the two harnesses where the
plugin and its MCP server work today:

- **Claude Code:** `/plugin marketplace add aac/act`, then `/plugin install act@act`.
- **Codex:** `codex plugin marketplace add aac/act`, then `codex plugin add act@act`. The
  Codex manifest points at the bundled skill and MCP server.
- **No plugin manager?** Point your agent at this repo (`github.com/aac/act`) and let it
  install whatever way fits its environment.

Cowork, the Claude Desktop app, and claude.ai aren't supported hosts yet: they can't launch
the plugin's MCP server the way the CLI harnesses do. Support for them is a planned
addition, not a requirement for anything above.

## How agents use it

`act` exposes its command set as an [MCP](https://modelcontextprotocol.io) server
(`act mcp`, stdio transport) so any MCP-aware agent can drive the loop without shelling
out. Most MCP tools mirror a CLI command (`act_ready`, `act_create`, `act_close`, `act_next`,
`act_finish`, …), so either interface drives the loop identically — `act_next`
(ready + claim + show) and `act_finish` (close + push) have matching `act next` /
`act finish` verbs like the rest.

## Status

`act` is pre-v1 but battle-tested — the tracker behind a dozen-plus active projects, and it
tracks its own development on its own backlog. The nested-repo layout and single-machine
workflow are thoroughly exercised and are the most-traveled path. Multi-machine
coordination over a git remote (`act remote`) is implemented and is the newest part of the
tool. Being pre-v1, act favors clean redesign over backward-compatibility shims.

## Privacy / telemetry: none

`act` is local-only. It collects no telemetry, phones home to nothing, and makes no network
calls except the git operations you explicitly invoke (e.g. `git push`, or `git pull` on
the nested `.act/` repo when syncing). See [SECURITY.md](SECURITY.md) for the full model.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
