# CLAUDE.md — act repo

This repo dogfoods `act` on its own backlog. Agents are the primary user; this file is the agent-runtime layer on top of `act help`.

## First session, every session

1. `./bin/act help` if you've never used `act` before. Mechanics live there; this file is for opinions and project-specific rules. If `bin/act` is missing, `go build -o bin/act ./cmd/act`.
2. `./bin/act ready` to see the unblocked frontier. Pick the highest-priority issue.
3. `./bin/act update --claim <id>` to take it.
4. Do the work, write tests, run them.
5. `git commit -m "<summary> (<short-id>)"` — the marker enables `act doctor` orphan-close detection.
6. `./bin/act close <id> --reason "<one-liner>"`.
7. `git push origin main` — concurrent agents see the close immediately; session-death can't lose work.
8. Repeat from step 2 until `act ready` is empty.

In an MCP-equipped session, prefer `act_next` + `act_finish` over the raw CLI; `.mcp.json` wires this up automatically.

## When to halt and surface to Andrew

The work loop is autonomous by default. Halt and ask only when:

- **Spec ambiguity:** acceptance criteria conflict, are missing a load-bearing detail, or two reasonable interpretations would produce visibly different code.
- **Breaking change:** a fix can't be made strictly additive — existing callers would have to change. Andrew decides whether to take the breakage or design around it.
- **Cross-issue scope:** the right fix needs another currently-open issue's fix to land first. File the dep and surface it; do not silently expand scope.
- **Deeper defect:** tests for the current issue reveal a bug bigger than the issue's description. File a follow-up, decide whether the current issue still makes sense to land standalone, surface if not.
- **External obligation:** anything cross-repo or genuinely public-facing — publishing a release, pushing a tag, opening a PR against another repo, sending notifications. *Pushing same-branch commits to origin is part of the loop (step 7), not an obligation to halt on.*

If you're unsure whether a situation qualifies, lean toward halting. Cheap to ask, expensive to undo.

## Mid-flight discoveries

Bugs and surface gaps you hit *while working a different issue* go straight into the backlog as follow-ups; they do **not** halt the current task:

```
./bin/act create "<title>" --type bug \
    --description "<repro + when discovered>" \
    --accept "<resolution criterion>"
```

Pattern: file it, keep working. If the discovery actually blocks the current issue, that's the "cross-issue scope" escape condition above; halt.

## Commit discipline

- Every work commit includes the issue's `(act-XXXX)` marker.
- Group `act create` / `act update` / `act dep add` ops with their work commit when they're load-bearing for the issue (e.g. filing follow-ups for an unresolved acceptance criterion). Otherwise let the auto-commit per `act` op stand on its own.
- Use `--no-commit` only for true bootstrap or migration cases where bundling is the right unit. Default is one op per commit.

## Sub-agents

Whether to spawn sub-agents is a harness decision, not an `act` rule. For this repo specifically: most v0.2 work touches `cmd/act/` and `internal/cli/` (argparser, error envelope, command dispatcher), so parallel agents will merge-conflict. Default serial. Spawn parallel only when issues are provably disjoint (e.g. one MCP-only change next to a CLI-only change with no overlapping files).

## What this file is not

Not a tutorial — `./bin/act help` is the tutorial. Not an architecture doc — `docs/spec-v2.md` and the brief are. This file captures *recommendations and rules for working in this repo* that don't fit elsewhere. After 3–5 sessions of real use, stable patterns extract into a global skill triggered by the presence of `.act/`.

## Versioning rationale

Each rule below is versioned so a later skill-extraction pass can decide what's load-bearing. New rules should be added inline with their rationale.

- *Halt on breaking changes* (2026-05): `act` is pre-v1; we still have freedom to redesign cleanly. Better to surface the question once than carry compat shims for a single user's convenience.
- *File mid-flight bugs as follow-ups, don't halt* (2026-05): the dogfood signal is the bug landing in the backlog with a clear repro, not a half-finished current task.
- *Default serial sub-agents in this repo* (2026-05): v0.2 ergonomic issues all touch CLI code. Parallelism would cost more in merge time than it saves.
- *Push after every close, not at session end* (2026-05): matches the dispatcher pattern, makes closes visible to concurrent agents immediately, and means a dropped session never silently swallows finished work. Verbose git history is the accepted cost. Discovered when the first dogfood agent (act-6bbd) followed the original loop and didn't push, leaving 3 commits local-only — see act-ac52.
