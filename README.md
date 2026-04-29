# act — Agent Task Tracker

A single-binary, git-resident task tracker designed so AI coding agents are the primary user. `act` lives inside any git repo, persists state as an append-only op log that merges cleanly across concurrent writers, and exposes its full surface as both a CLI and an MCP server. Humans interact with the work through the agents, not the tracker. Inspired by Beads, deliberately scoped down for solo-to-small-multi-agent use without external services.

## Status

This repo's build is in progress and agent-driven. No code has been written yet — the repo currently holds the project definition and the dispatcher prompt that the build pipeline runs against.

- `docs/brief.md` — project definition (north-star thesis, data model, command surface, multi-writer semantics, definition of done)
- `docs/dispatcher-prompt.md` — dispatcher prompt for the agent-driven build pipeline
- `docs/STATUS.md` — current build state (created once the pipeline begins)

## Repo layout

- `cmd/act/` — binary entry point (the `act` command).
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
- `docs/` — project brief, spec, and per-issue task files. See [`docs/spec-v2.md`](docs/spec-v2.md) for the authoritative design.
- `testdata/` — fixtures and snapshot golden files.

The full build DAG lives in [`docs/issues/INDEX.md`](docs/issues/INDEX.md).
