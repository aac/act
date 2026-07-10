# act — project setup & install

Read this when bringing act into a new project, or when act isn't yet installed in your
environment. Once a project is configured, none of this content needs to be in context —
SKILL.md handles the per-iteration loop.

## Finding (or installing) the binary

The skill always calls plain `act`. Find it in this order:

- **`act` on `PATH`** — the normal case. The plugin install puts its bundled `bin/act` on
  `PATH`; `go install` also lands it on `PATH`.
- **`${CLAUDE_PLUGIN_ROOT}/bin/act`** — the plugin's bundled binary, if `PATH` resolution
  misses it.
- **`./bin/act`** — a project-local build, common when developing act itself
  (`go build -o bin/act ./cmd/act`).
- **Nothing found** — if you're in the `act` repo, build it; otherwise surface to the
  human, since act must be installed before the loop can run.

If the project's `.mcp.json` wires the MCP tools (`act_next` / `act_finish` / …), prefer
them over the CLI — they bundle loop steps into single calls and return `commit_marker`
without a subprocess. Run `act help` once at the start of a session for the full command
reference; this file is the setup layer, SKILL.md the loop layer.

## A note on push permission prompts

act pushes on every close, so concurrent agents see finished work immediately. If your
agent harness asks for permission before running commands, that per-close `git push` may
prompt for confirmation. Allowing it is a harness/permission-config concern, not an act
setting — grant it wherever you configure your agent's permissions.

## Codex sandbox — `.act/` writability

If the harness is **Codex** (container-based sandbox), the one act-specific setup check is
that act's nested `.act/` repo falls inside the container's **declared writable root** —
otherwise act commands can't write the op-log. `.act/` lives at the project root, so it's
within the writable root by default; confirm only if `.act/` is mounted separately or at a
non-standard path.
