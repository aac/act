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

`act init` never commits to the host repo. It writes the `.act/` ignore entry and a
pre-commit hook into your working tree and leaves them for you to review; pass
`--commit-host` if you want act to make that commit. It also never edits `CONTRIBUTING.md`
unless you pass `--contributing`.

## What a session looks like

```sh
$ act ready
act-3c89 2 cli: act show --full to disable description and reason truncation
act-7ecd 2 cli: act close --reason validates length upfront
act-4b45 2 cli: act ready shows assignee and claimed_at columns

$ act update --claim act-3c89    # atomic; concurrent claimers resolve last-write-wins
# ...write the code, run the tests...
$ act close act-3c89 --reason "added --full flag; tests cover both truncation paths"
$ git commit -m "act show --full disables truncation" -m "Act-Id: act-3c89" \
    -- internal/cli/show.go internal/cli/show_test.go   # name the paths; -a would
                                                        # sweep in a sibling
                                                        # session's dirty files
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

`act` exposes its work loop as an [MCP](https://modelcontextprotocol.io) server
(`act mcp`, stdio transport) so any MCP-aware agent can drive it without shelling out.
Eight tools are advertised — `act_next` (ready + claim + show), `act_finish`
(close + push), `act_block`, `act_file_blocker`, `act_list`, `act_show`, `act_create`,
`act_update` — each mirroring a CLI verb of the same name, so either interface drives the
loop identically. Setup and diagnostic verbs (`act init`, `act doctor`, `act search`,
`act ready`, `act close`, `act dep add`, `act log`, `act version`) are CLI-only: every
advertised tool's schema is re-read on each agent turn, so the MCP surface carries only
what the loop runs.

## Reading many stores at once

Read commands (`ready`, `list`, `show`, `log`, `search`) normally refresh the tracker first:
a `git pull --rebase` inside the store's own `.act/.git`, which — when it moves HEAD — drops
the fold checkpoint and index so the next read rebuilds. That is the right default for one
repo you are working in, and the wrong one for a sweep: N stores means N rebases into repos
other agents may be writing to right now.

Pass `--no-fetch` (or set `ACT_NO_FETCH=1` for a whole sweep) to read without touching the
store at all — no fetch, no rebase, no checkpoint or index deletion. Every read reports what
the refresh layer did under a `refresh` key, including `age_seconds` — how stale the answer
is — so an aggregator never has to stat `FETCH_HEAD` itself:

```sh
$ ACT_NO_FETCH=1 act ready --json | jq .refresh
{ "reason": "no_fetch", "fetched": false, "age_seconds": 412 }
```

A refresh that *fails* (unreachable remote, no network) is no longer silent: the command
still answers from on-disk state and still exits 0, but `refresh.error` names the failure
and a `WARNING` goes to stderr. Before, a failed refresh looked exactly like a successful
one.

## If a write is interrupted

Every act write auto-commits to the nested `.act/` git repo. If that commit is
interrupted partway — the session dies, Ctrl-C lands mid-write, the disk fills,
or a sandbox denies git a file operation — git can leave a stale lock file
(`index.lock` or `HEAD.lock`) in `.act/.git/`. While a stale lock is present,
every act write fails until the lock is removed. The failed write reports a
structured `stale_git_lock` error naming the lock file and the recovery
sequence, and `act doctor` detects the lingering lock as an error finding (it
also catches the index divergence that tends to follow).

Nothing is lost in this state: the failed write's op file is already on disk,
and the op files — not the nested git history — are the source of truth.

To recover:

```console
$ rm -f .act/.git/index.lock .act/.git/HEAD.lock
$ git -C .act add ops && git -C .act commit -m "recover ops stranded by a stale lock"
$ act doctor --fix
```

A note for sandboxed environments that gate file deletion (Claude's Cowork,
for example): a denied delete can make git's own temp-file cleanup fail
mid-commit and leave exactly this state — and the same denial then blocks
removing the lock. Grant the sandbox's file-deletion permission for the
project folder before pointing act at it.

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
