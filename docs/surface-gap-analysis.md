# Surface gap analysis — act v0.1.0

Date: 2026-04-30
Scope: 13-command CLI (`init, create, list, show, update, close, dep add, ready, search, log, doctor, mcp, version`) plus 3 composed MCP tools (`act_next, act_finish, act_block`). References: `/home/user/act/docs/spec-v2.md`, `/home/user/act/docs/brief-v4.md`.

This is a breadth-first walkthrough of four representative agent-driver workflows. The goal is to find places where the surface forces an agent to chain multiple commands when one would do, where load-bearing information is reachable but not surfaced, and where op types exist without a CLI verb to drive them.

## Workflow A — File a bug after a failed test run

The agent has just finished a test run, sees a failure in `parser_test.go::TestEdgeCase`, wants to file a bug, link it as a blocker on the work-in-flight, and continue.

### Steps the agent runs today

1. `act create "test failure: parser edge case" --type bug --priority 0 --description "stderr tail: ..." --json` → returns new id `act-77ab`.
2. `act dep add <wip-id> act-77ab --type blocks --json` (so `wip` is blocked by the bug). Direction is non-obvious; see gap below.
3. `act update <wip-id> --status blocked --json` to mark wip itself blocked. (Or: `act_block` composed tool combines steps 2 and 3 — but only after step 1.)
4. Continue.

### Where the surface feels right

- `--json` everywhere, structured output suitable for scripting.
- `act_block` correctly composes status flip + dep-add — the right shape for the link step.
- `--type bug` and `--priority 0` are agent-friendly first-class flags.

### Where it feels missing or awkward

- The agent must remember the wip id from when it claimed the issue. There is no `act mine` / `act current` to ask "what am I working on?" The `list --assignee=$me --status=in_progress` workaround is fine but verbose and requires the agent to know its own assignee string. (Annoying.)
- Filing-and-linking is two non-atomic calls (create, then dep add). If the second fails for any reason, the bug exists with no edge to the wip. There is no `act create --blocked-by <id>` flag and no `act_file_blocker` composed MCP tool. (Annoying.)
- The failing test stderr tail is multi-line; passing it as a single shell-escaped `--description` string is awkward. There is no `--description-file <path>` flag. (Annoying.)
- `act dep add child parent` semantics: the doc string uses `parent` to mean "thing that blocks." This collides with the `issue.parent` field used for hierarchy. The terminology asks the agent to think twice about edge direction — easy to file the wrong way. (Annoying.)

### Severity tally

- Annoying: missing `--blocked-by` on `create`, no `act_file_blocker`, no `act mine`, no `--description-file`, dep-add direction terminology.

## Workflow B — Claim work, do it, ship it

Agent calls `act_next`, gets an issue, makes a code commit referencing it, closes it.

### Steps the agent runs today

1. `act_next` → `{claimed: true, issue: {id: "act-a1b2...", prefix: "act-a1b2", ...}}`.
2. Agent edits code.
3. `git commit -m "fix parser bug (act-a1b2)"` — must remember to include the `(act-XXXX)` marker so `act doctor orphan-close` can correlate.
4. `act_finish act-a1b2 --reason "shipped, tests green"` (which writes the close op + an op-commit whose message includes the marker, satisfying the doctor check from a separate angle).

### Where the surface feels right

- `act_next` is the clean composed primitive. Output gives the agent the prefix it needs.
- `--reason` provides the "what I did" note at close time. It is free text and load-bearing for audit (Workflow D); the spec confirms `closed_reason` is what `act doctor` uses to distinguish abandoned vs completed.
- `act_finish` includes the marker in its op-commit message, so the doctor check survives even if the agent's *own* commit is missing the marker.

### Where it feels missing or awkward

- `act_next` returns `id` and `prefix` but does not return a ready-to-paste `commit_marker: "(act-a1b2)"` string. The agent has to construct it. Two ways to get this wrong: forget the parens, forget the prefix prefix `act-`. (Annoying.)
- The agent's *code commit* (step 3) is purely convention — `act` does nothing to enforce or assist. There is no `act commit-marker <id>` helper to pipe into a commit-msg template, and no documented `prepare-commit-msg` hook integration. The doctor check passes because `act_finish`'s op-commit has the marker, but the *evidentiary link* between the work commit and the issue is purely manual. (Annoying.)
- `--reason` is the only "what I did" capture surface and it is a single string field. That is fine for v1 (the brief explicitly drops comments), but it is worth flagging.

### Severity tally

- Annoying: `act_next` should emit a ready-to-use commit marker; no `act commit-marker` CLI helper.

## Workflow C — File a fix, then file a follow-up

Mid-implementation of X (`act-aaaa`), the agent finds a refactor opportunity. It files Y as a follow-up that should not be picked up until X ships.

### Steps the agent runs today

1. `act create "refactor parser after X" --type chore --json` → `act-bbbb` (Y).
2. `act dep add act-bbbb act-aaaa --type blocks` (Y is the child, X is the parent that blocks Y). Direction-mental-load: the agent must read this as "Y depends on X" / "Y is blocked by X."
3. Ship X via `act_finish act-aaaa --reason "..."`.
4. After X closes, Y appears in `act ready` because no open `blocks` edge points at it.

### Where the surface feels right

- The `blocks` edge does the right thing for `ready`. After X closes, Y unblocks deterministically.
- `act ready` is a one-shot answer to "what's next?"; agent doesn't need to filter manually.

### Where it feels missing or awkward

- Same `--blocked-by` gap as Workflow A. A single `act create "title" --blocked-by act-aaaa` invocation should be the obvious shape. Two-step is friction every time. (Annoying.)
- Same direction-terminology gap on `act dep add`. (Annoying — counted once.)
- After X closes, the agent has no "did anything I filed unblock?" query. `act ready` works but the agent has to remember to call it; there is no `--watch-children` or notification path. The brief explicitly defers pubsub, so this is correct-by-design. (Not a gap.)

### Severity tally

- Annoying: same `--blocked-by` gap rolling up from Workflow A.

## Workflow D — Audit a closed issue

A week later, the agent (or human-via-agent) wants to know: who claimed `act-a1b2`, when did it close, and what was the close reason?

### Steps the agent runs today

1. `act show act-a1b2 --json` → returns `{assignee: "agent-1", closed_at: "...", closed_reason: "shipped"}`.
2. To confirm *who actually ran the close op*: `act log act-a1b2 --json` → returns the op stream; agent grep-finds the `close` op and reads its `node_id` / `writer_version`.

### Where the surface feels right

- `closed_at` and `closed_reason` are first-class fields on the snapshot. That is exactly the audit affordance the brief promises.
- `act log` is complete: every state change with HLC, node_id, payload. Forensics-grade.

### Where it feels missing or awkward

- `act show` does **not** surface `closed_by_node` (the node_id of the writer that actually closed the issue). The folded snapshot has `assignee` (last value, which can drift after close — an `update --assignee` after close is a real op stream possibility) but no `closer`. To answer the most basic audit question — "who closed this?" — the agent must drop to `act log` and grep. This is load-bearing missing. (Critical.)
- Similarly no `claimed_by_node` / `claimed_at` on the snapshot. The agent reconstructs it from log. (Annoying — `assignee` is a reasonable proxy in the common case, but not authoritative.)
- `act log` is verbose. For a quick audit "show me the timeline," there is no `--summary` mode that emits one line per op (`hlc, op_type, node_id, brief payload`). (Nice-to-have.)
- `closed_by_tree` (the git tree hash captured at close time per spec line 681) is computed and stored on the index but never surfaced through `act show`. For deeper forensics (which commit *was* the close associated with?), this is valuable and free. (Nice-to-have.)

### Severity tally

- Critical: `act show` missing closer identity.
- Annoying: no claimant identity on snapshot.
- Nice-to-have: `act log --summary`, `closed_by_tree` on `act show`.

## Cross-cutting gaps (not workflow-specific but surfaced by the walkthrough)

- **`reopen` op type has no CLI verb.** Spec §5.B.4 specifies the semantics; `act update --status open` on a closed issue is explicitly rejected by §5.A.4. There is no way to reopen a regressed bug without writing an op file by hand. (Critical.)
- **`redact` op type has no CLI verb.** Used for retraction of sensitive content; no `act redact` command. (Annoying.)
- **`tombstone` op type has no CLI verb.** No `act delete <id>` for issue-level deletion. (Annoying.)

## Aggregate prioritized gap list

Critical (blocks a load-bearing audit or recovery flow):

1. `act show` does not include closer identity (`closed_by_node`, optionally also `closed_by_tree`).
2. No `act reopen <id>` command despite `reopen` being a first-class op type.

Annoying (workable today, but adds friction every time):

3. No `act create --blocked-by <id>` flag and no `act_file_blocker` composed MCP tool.
4. No `act mine` / `act ready --mine` for "what am I currently working on / what's next for me."
5. `act dep add` direction terminology overloads `parent`; needs `--blocks` / `--blocked-by` aliases.
6. No `--description-file <path>` for `create` / `update`; multi-line content via shell strings is fragile.
7. `act_next` output does not include a ready-to-paste `commit_marker` string.
8. No `act redact` CLI command.
9. No `act delete <id>` CLI command (tombstone op).

Nice-to-have (small ergonomics wins):

10. `act log --summary` / `act show --history` for one-line-per-op timelines.

That is 10 gaps total: 2 critical, 7 annoying, 1 nice-to-have. Workflows B and C are mostly right. Workflows A and D have the most surface friction.

## Filed gap issues

Each item above is filed as `act-XXXX-gap.md` under `/home/user/act/docs/issues/`:

- act-g001-gap: `act show` closer identity
- act-g002-gap: `act reopen` command
- act-g003-gap: `act create --blocked-by` and composed `act_file_blocker`
- act-g004-gap: `act mine` / `act ready --mine`
- act-g005-gap: `act dep add` direction aliases
- act-g006-gap: `--description-file` for create and update
- act-g007-gap: `act_next` includes commit_marker
- act-g008-gap: `act redact` CLI command
- act-g009-gap: `act delete` CLI command (tombstone)
- act-g010-gap: `act log --summary` timeline view
