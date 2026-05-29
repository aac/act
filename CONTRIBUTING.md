# Contributing to act

Before changing code, read [`AGENTS.md`](AGENTS.md). It is the engineering guide
for this repo and covers the rules that govern what gets merged:

- **Documentation discipline.** Every user-visible behavior claim in a doc must
  ship with a test that asserts the behavior at the user-visible boundary, in the
  same commit. See the "Documentation discipline" section of `AGENTS.md` for what
  counts as user-visible and the `TestDocClaim_*` convention.
- **Versioning rationale.** `act` is pre-v1; the project favors clean redesign over
  backward-compat shims. The rationale archive in `AGENTS.md` records why specific
  decisions were made.

Run `go test ./...` and `gofmt -l` before opening a PR.

## MCP configuration

The repo ships a `.mcp.json` pointing at the installed `act` binary (`"command": "act"`). This is the correct shape for Claude Code and Codex once `act` is on `PATH` via `go install github.com/aac/act/cmd/act@latest`.

When developing against an uncommitted build, override locally — do **not** edit the tracked `.mcp.json`. The simplest approach is a project-local `settings.local.json` or a shell alias. Alternatively, rebuild to `./bin/act` and point a gitignored override file at it:

```json
{
  "mcpServers": {
    "act": {
      "command": "./bin/act",
      "args": ["mcp"]
    }
  }
}
```

Save that as `.mcp.local.json` (gitignored) and load it with `claude --mcp-config .mcp.local.json`, or temporarily swap `.mcp.json` without committing the change.

<!-- act:contributing-stanza:start -->

## act commit-marker convention

This repo uses [act](https://github.com/aac/act) for agent task tracking.
Agent-authored commits include an `Act-Id: act-XXXX` trailer in the commit
body that pairs the commit with its tracked issue.

You don't need to interact with this convention — `Act-Id:` trailers are
ignored by conventional-commit linters, semantic-release, and CHANGELOG
generators, and have no effect on merge or review. If you'd like to add
them to your own commits, see act's docs; otherwise, ignore.

<!-- act:contributing-stanza:end -->
