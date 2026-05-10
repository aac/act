# act — Agent Task Tracker

A single-binary, git-resident task tracker designed so AI coding agents are the primary user. `act` lives inside any git repo, persists state as an append-only op log that merges cleanly across concurrent writers, and exposes its full surface as both a CLI and an MCP server. Humans interact with the work through the agents, not the tracker. Inspired by Beads, deliberately scoped down for solo-to-small-multi-agent use without external services.

## Getting started

```sh
go install github.com/aac/act/cmd/act@latest
cd <your project>            # any git repo
act init                     # creates .act/, records a node_id
act help                     # the agent-onboarding tutorial
```

That's the whole bootstrap. `go install` requires a Go toolchain (1.25+), which most coding-agent environments already have; no daemon, no external service, no schema setup. The op log lives under `.act/` in your repo and merges across concurrent writers with plain `git pull --rebase`.

From a Claude Code session, the global `act` skill at `~/.claude/skills/act/SKILL.md` auto-activates whenever `.act/` is present, so the agent picks up the canonical work loop without further setup. From any other context, `act help` plus `act help workflow` are enough to drive the loop from a CLI alone.

### Why `go install`

It's one command from a fresh agent session and the only path that needs no environment-specific setup (no tap, no shell-script trust prompt, no manual download/chmod/place-on-PATH). The Go-equivalent of the `uvx` pattern.

Alternates exist for environments where `go install` isn't the right fit, but they are alternates, not the canonical pitch:

- **Build from source** — `git clone github.com/aac/act && cd act && go install ./cmd/act`. The dogfood path inside this repo, and the workaround when `@latest` is stale relative to HEAD.
- **GitHub release binary** (tracked in act-4fe6 for CC Web, act-8416 for Cowork) — prebuilt binaries for sandboxed environments without a Go toolchain. Higher friction (download + chmod + PATH) but doesn't require Go on the host.
- **Homebrew tap / curl installer** (tracked in act-e6a5) — `brew install aac/act/act` once the tap exists. Lower friction for humans on macOS, higher friction for agents because tap setup adds a confirmation step.

## Status

Feature-complete for the v0.1 surface; actively dogfooded on this repo's own backlog. See `docs/spec-v2.md` for the authoritative design, `docs/act-evaluation.md` for the live evaluation, and `act help` for the agent-facing tutorial.

## Repo layout

- `cmd/act/` — binary entry point.
- `internal/` — non-exported packages, one per spec section:
  - `canonicaljson/` — RFC 8785-style canonical JSON.
  - `hlc/` — Hybrid Logical Clock.
  - `ids/` — task and op id generation and resolution.
  - `op/` — op envelope schema and payload types.
  - `store/` — on-disk `.act/` op log layout.
  - `fold/` — deterministic op-fold algorithm.
  - `index/` — SQLite read index.
  - `cli/` — CLI command wiring.
  - `config/` — `.act/config.json` loader.
  - `hooks/` — pre-close gate runner.
- `docs/` — spec, evaluation, session handoffs.
- `testdata/` — fixtures and snapshot golden files.

## Roadmap pointers

- Open issues: `act ready` in this repo, or read the op log under `.act/ops/`.
- Decisions and rationale: per-issue task files in `docs/issues/` and `CLAUDE.md`'s "Versioning rationale" section.
