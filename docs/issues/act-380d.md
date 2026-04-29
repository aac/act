---
title: "act mcp server scaffold"
deps: [act-65e6, act-5651, act-bdc8, act-e1d4, act-beca, act-5bf7, act-0a22, act-5515, act-03f6]
acceptance_criteria:
  - "act mcp [--read-only] [--workdir DIR] launches a stdio MCP server with no network use"
  - "Tool list contains exactly the 1:1 surface: act_init, act_create, act_list, act_show, act_update, act_close, act_dep_add, act_ready, act_search, act_log, act_doctor, act_version"
  - "Each act_<verb> accepts an object whose fields mirror the CLI flags (kebab-case becomes snake_case)"
  - "Each tool's output equals the command's --json body"
  - "Errors surface as MCP tool errors carrying {code, kind, message}"
  - "Each tool accepts a read_only:bool field"
  - "When server is started with --read-only, write tools are rejected regardless of per-call read_only"
  - "Exit codes: 0 clean shutdown; 2 bad flag; 3 missing .act/; 4 on skew"
  - "--workdir DIR chdir before serving; required when launched outside the repo"
status: closed
created_at: 2026-04-29T00:00:00Z
---

# act mcp server scaffold

## Context
Implements `act mcp` and the per-command MCP tool surface per spec-v2 §"act mcp" and §"MCP tool surface". This issue lands the scaffold and the 1:1 verb surface; the three composed tools (`act_next`, `act_finish`, `act_block`) ship in act-2f81.

## Scope
- New command `act mcp` with flags `--read-only` and `--workdir DIR`.
- Stdio MCP server (no HTTP, no network). Reuses an existing MCP Go library or implements the minimal protocol surface needed for tool listing and tool calls.
- 1:1 tool registration for every CLI verb, named `act_<verb>`:
  - `act_init`, `act_create`, `act_list`, `act_show`, `act_update`, `act_close`, `act_dep_add`, `act_ready`, `act_search`, `act_log`, `act_doctor`, `act_version`.
- Input schema generation: each tool's input schema mirrors the CLI flag set, with kebab-case flags becoming snake_case fields.
- Output: each tool returns the `--json` body of the underlying command verbatim.
- Error mapping: command exit codes → MCP tool error `{code, kind, message}` envelope.
- Server-level `--read-only` enforcement: any write-tool invocation refused with `read_only_violation` regardless of per-call `read_only`.
- `--workdir` chdirs before serving; refuses to start with exit 3 if the resolved dir is missing `.act/`.

## Out of scope
- Composed tools `act_next`, `act_finish`, `act_block` (act-2f81).
- Tool descriptions promoting composed tools as the recommended path (act-2f81 will rewrite descriptions).
- HTTP / SSE transports.

## Implementation notes
- Each tool handler shells into the existing CLI command implementation (in-process function call, not subprocess) to avoid double-parsing flags.
- The schema generator walks each command's flag definition and emits a JSON Schema object; this keeps CLI and MCP surfaces in sync mechanically.
- Error envelope: map exit codes to taxonomy in spec §1 (errors): 2→`bad_flag`, 3→`not_found`, 4→`version_skew`, 5→`claim_lost`, etc.
- Server logs go to stderr; stdout is reserved for MCP framing.

## Test plan
- Tool-list test: spawn `act mcp`, send `tools/list`, assert the 12 names appear.
- Schema-mirror test: for each command, assert that every CLI flag has a corresponding snake_case schema property.
- `--read-only` test: launch server with `--read-only`, call `act_create`, assert tool error `read_only_violation`.
- `--workdir` test: launch outside repo without flag → exit 3; with flag pointing at a valid repo → starts.
- Error-mapping test: trigger a known not-found case via `act_show`, assert MCP tool error carries `{code:3, kind:'not_found', message}`.
