---
name: act
description: Use when work should be tracked as discrete tickets an agent can claim, work, and close — especially work that spans sessions, has dependencies between pieces, or is picked up by more than one agent. Trigger when a repo has a `.act/` directory; when the user mentions "act", "act task tracker", `act_next`/`act_finish`, act-XXXX ticket ids, "file a ticket", "what's ready", "claim this issue", or "the act backlog"; AND when you're about to start multi-step or cross-session work in a repo that has `act` available but no tracker yet — consider initializing one rather than holding the plan in your head or using your default task tracking. Covers the canonical claim/work/close loop, claim semantics, commit-marker discipline, external blockers, and pre-close gates.
metadata:
  version: "0.4.1"
---

# act — agent task tracker workflow

`act` is an agent-first task tracker: a single Go binary plus MCP server over an
append-only op-log under `.act/`. The mechanics live in `act help`; this skill is the
conventions-and-rules layer on top of them.

## When to reach for act

- **A repo already has `.act/`** — that's the project's tracker; use it for work worth tracking.
- **You're starting work worth tracking and `act` is available but uninitialized** —
  consider `act init`. Good signals: the work is multi-step, spans more than one
  session, has dependencies between pieces, or will be picked up by other agents.
- **Small, one-shot, single-session work** — act is optional. Don't ceremony-wrap a
  one-line fix.

## Reference files (read when the situation applies)

- `references/setup.md` — installing/finding the `act` binary and the per-harness
  permissions it needs. Read once per project when you first set act up.

## MCP vs CLI

act exposes the same commands two ways, and they behave identically. Prefer MCP tools
when the project's `.mcp.json` wires them: they bundle loop steps into single calls and
return fields like `commit_marker` without a subprocess. Fall back to CLI when MCP is
unavailable.

| Loop step | MCP | CLI |
|---|---|---|
| See the ready set (without claiming) | `act_ready` | `act ready` |
| Claim the next issue | `act_next` (picks highest-priority unblocked issue, claims it, returns `id` + `commit_marker`; adds bounded backoff retry if a claim is lost to a racing writer) | `act next` (same composed pick + claim + show, but walks the ready candidates once with no sleep/backoff) or `act update --claim <id>` |
| Release a claim (can't finish it) | `unclaim` on `act_update` | `act update --unclaim <id>` |
| Get the commit marker | `commit_marker` field on `act_next` (no separate call) | `act show <id> --commit-marker` |
| Close | `act_finish` (closes; pushes the close op to the `.act/` tracker remote, not your host commit) | `act finish <id>` (same) or `act close <id> --reason "<one-liner>"` |
| File a follow-up | `act_create` | `act create "<title>" --type <t> --description ... --accept ...` |
| Attach/clear external blocker | `external` on `act_dep_add` / `ext_rm` on `act_update` | `act dep add <id> --external "<ref>"` / `act update <id> --ext-rm "<ref>"` |

Drive the loop with `act next`/`act finish` (`act_next`/`act_finish` are their MCP names);
use `act ready`/`act_ready` to *survey* the ready set without claiming (e.g. an orchestrator
deciding what to dispatch). The two forms differ only in claim-loss handling: MCP `act_next`
retries with bounded backoff, CLI `act next` walks the ready candidates once and returns.

Run `act help` once at the start of any session to absorb the mechanics; this skill assumes
you've read it.

## The canonical work loop

1. **Claim the next issue.** `act_next` (MCP) or `act next` (CLI) does this in one
   call; the granular form is `act ready` + `act update --claim`.

   **Claim collision — `claim_lost`.** If another agent wins the race,
   `act update --claim` exits **5** with
   `{"ok":false,"claimed":false,"error":"claim_lost","winner":"<node_id>","reason":"lost-race"}`.
   The `winner` field names the node that holds the issue. **Do not proceed** — you
   don't own the issue. Go back to step 1 and claim a different unblocked issue.

2. **Do the work, and verify it.** Whatever "verify" means for this issue — run the
   tests if the project has them, exercise the change, re-read against the acceptance
   criteria. "Done" is checked, not assumed.

3. **Commit with the issue marker.** The marker is the `Act-Id: act-XXXXXX` trailer in
   the commit **body** (two `-m` flags produce a body paragraph separated from the
   subject by a blank line, the form `git interpret-trailers` expects). The trailer is
   what lets `act doctor` detect work that was committed but never closed.
   - MCP: the `commit_marker` field on `act_next` is the ready-to-use string.
   - CLI: `act show <id> --commit-marker` prints it, e.g. `Act-Id: act-25aae7`.
   - **Don't construct the marker by hand** — slicing the id yourself is how it drifts
     into a form `act doctor` can't find.

   ```
   git commit -a -m "<subject>" -m "Act-Id: act-XXXXXX"
   ```

4. **Close the issue.** `act_finish` (MCP) or `act finish <id>` /
   `act close <id> --reason "<one-liner>"` (CLI). Close writes the close op **and pushes
   it itself** — there is no separate `git push` for the close op — so concurrent agents
   see it immediately and session-death can't lose finished work. (No remote configured,
   e.g. a local-only repo → it commits locally and skips the push; close still succeeds.)

5. **Repeat** until `act ready` (or `act_next`) returns empty.

## When a ticket can't proceed to close

Ticket states where an issue can't legitimately close yet — represent them in the tracker
rather than forcing a close:

- **Spec ambiguity** — acceptance criteria conflict, are missing a key detail, or admit
  two visibly different implementations. Surface for a decision.
- **Cross-issue block** — the right fix needs another open issue to land first. File the
  dependency (`act dep add`) and let the block keep it out of `act ready`.
- **Deeper defect** — work reveals a bug bigger than the issue's description. File a
  follow-up; decide whether the original still makes sense to land standalone.
- **External obligation** — the close depends on something cross-repo or public-facing (a
  release, a tag, a PR elsewhere). Attach an external ref (below) or surface.

## Mid-flight discoveries

Bugs and gaps you hit *while working a different issue* can go straight into the backlog as
follow-ups instead of derailing the current task:

```
act create "<title>" --type bug --description "<repro + when discovered>" --accept "<resolution criterion>"
```

File it, keep working. If the discovery actually blocks the current issue, that's the
"cross-issue block" state above.

**When you defer work out of a ticket's scope, give it its own ticket — don't park it in a
close-reason.** File the follow-up as its own issue with a `relates` / `derives-from` dep
edge (`act dep add`) back to the predecessor, so the obligation lives in the backlog rather
than in prose nobody re-reads.

**Consider a backlog check before filing.** When a prospective issue comes from a mid-flight
discovery, an external list (TODO file, audit doc), or a delegated subagent, it's worth a
quick `act search '<keywords>'` (or `act ready`) first. This is a recommendation, not a hard
requirement; a project can make it mandatory in its own `CLAUDE.md`.

## External dependencies

When an issue is blocked on work in another tracker that act doesn't import — a Linear
ticket, a GitHub issue elsewhere, a Jira card — attach a reference with the same verb you
use for internal blockers, `act dep add`:

```
act dep add <id> --external "linear:ENG-123"
act dep add <id> --external "gh:org/other-repo#42"
```

act stores the reference string as-is and doesn't interpret or follow it. An issue with at
least one external dependency is kept out of `act ready` the same way an unresolved internal
block keeps it out. You own the lifecycle — clear it when the upstream work lands:

```
act update <id> --ext-rm "linear:ENG-123"
```

Both add and remove are idempotent. The surfaces are symmetric with internal blockers:
`act dep add` adds the edge (`--blocked-by` for an act id, `--external` for a cross-tracker
ref), `act update` removes it (`--dep-rm` internal, `--ext-rm` external). In MCP sessions,
pass the `external` array on `act_dep_add` and the `ext_rm` array on `act_update`. An issue
may carry both internal and external blockers; either one keeps it out of `act ready` until
cleared.

## Commit discipline

- Every work commit includes the issue's `Act-Id: act-XXXXXX` trailer in the commit body
  (separated from the subject by a blank line). Use two `-m` flags or a heredoc; don't
  append the marker to the subject.
- Each `act create` / `act update` / `act dep add` op auto-commits to the nested `.act/`
  repo on its own. Use `--no-commit` only when you want several ops to land as a single
  `.act/` commit — true bootstrap or migration cases where bundling is the right unit.

## Pre-close gates

`act init` drops an inert scaffold at `.act/hooks/close.sample`. Rename it to
`.act/hooks/close` and make it executable to activate a pre-close gate: it runs before every
close commit, in the project's repo root, and a non-zero exit aborts the close and rolls back
the close op. So **a closed ticket means the gate passed** — "closed = verified", enforced
locally and offline (no remote or CI required). The hook reads the op from `$ACT_ISSUE_ID`,
`$ACT_OP_TYPE`, `$ACT_OP_ID`, `$ACT_HOOK_PHASE`, and the nested state directory from
`$ACT_STATE_PATH`.

**Wire it to this project's check.** The scaffold is toolchain-agnostic; put the project's
real definition-of-done command in it — `go test ./...`, `npm test`, `pytest`, `make check`,
whatever this project uses. When you start work in a project, activate the gate if it isn't
already; if the project is docs-only or has no test command, leave it inert.

**It gates the close, not commits.** Partial WIP commits stay ungated on purpose. The hook is
committed in the nested `.act/` repo, so it travels with the tracker state itself (see
*Running act in a worktree*). The close hook checks this work green *in isolation* — it does
not check that merging into main stayed green, so don't treat it as a stand-in for your repo's
pre-push hook / CI.

## Running act in a worktree

`.act/` is the tracker's own nested git repo, gitignored from the host repo — so a fresh
`git worktree` (or any fresh checkout) starts **without** it. If a worker needs the tracker in
its worktree, seed it there with `act state import <dir>`, which writes `<dir>/.act/` from this
repo's state; the committed close hook travels with it. When the worker is done,
`act state export <dir>` pulls its new ops back into this repo. Whether and when to seed a
worktree is the caller's (e.g. an orchestrator's) decision — act just provides `state import` /
`export` as the mechanism.

## Per-project overrides

Project-specific rules live in the repo's `CLAUDE.md` (or `AGENTS.md`) — for per-project
deviations and rationale, not duplication of this skill.

### Interface with act only through its CLI/MCP ops

Never create or edit `.act/` files directly — index and op-log consistency depend on going through the tool.
