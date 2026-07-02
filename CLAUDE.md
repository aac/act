# CLAUDE.md

@AGENTS.md

The engineering guide for working on this codebase lives in [`AGENTS.md`](AGENTS.md) — imported above, so Claude Code loads it automatically at session start (Claude Code reads `CLAUDE.md`, not `AGENTS.md`, so the import is what carries the content into context). It covers the documentation-discipline rule, the versioning rationale, and the project-specific build conventions.

To *use* `act` as a task tracker (the canonical claim/work/close/push loop), install the plugin (`/plugin install act@act`) — the skill ships with it — and run `act help`.
