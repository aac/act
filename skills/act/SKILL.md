---
name: act
description: Use when working in any repo with a .act/ directory at its root, or when the user mentions "act task tracker", "act_next", "act_finish", act-XXXX issue ids, or agent-driven task management. Also trigger on phrases like "what's ready", "claim this issue", or "the act backlog". Covers the canonical work loop, claim semantics, commit-marker discipline, sub-agent isolation, and review patterns for any project that uses act for agent task tracking.
---

# act — agent task tracker workflow

This skill activates whenever you're in a repo that uses `act` (the agent-first task tracker — single Go binary plus MCP server, append-only op-log under `.act/`). It's the runtime layer for how to use the tool. Mechanics live in `act help`; this is the opinions and rules layer.

**Reference files (read when the situation applies):**

- `references/setup.md` — installing/finding the act binary, Claude Code auto-mode permission carveouts. Read once per project at bootstrap.
- `references/worktree-subagents.md` — the worktree `--push` trap and parallelism-vs-isolation guidance. Read before dispatching any sub-agent.

If act isn't already installed and configured in this project, read `references/setup.md` first. Otherwise proceed.

**MCP vs CLI:** When the project's `.mcp.json` wires `act_next`, `act_finish`, and related tools, **prefer MCP tools** — they compose canonical-loop steps into single calls and return fields like `commit_marker` without a subprocess. Fall back to CLI commands when MCP tools are unavailable. The skill describes both; all behavior is the same at the semantic level.

Run `act help` once at the start of any session to absorb the mechanics. That's the canonical reference; this skill assumes you've read it.

## The canonical work loop

The single discipline that makes everything else work:

1. **Claim the next issue.**
   - MCP: `act_next` — atomically picks the highest-priority unblocked issue, claims it, and returns `id` plus `commit_marker`.
   - CLI: `act ready` to see what's unblocked, then `act update --claim <id>`.

   **Claim collision — `claim_lost`.** If another agent wins the race, `act update --claim` exits **5** with envelope `{"ok":false,"claimed":false,"error":"claim_lost","winner":"<node_id>","reason":"lost-race"}`. The `winner` field names the node that holds the authoritative claim (last-write-wins fold makes the winner the single source of truth). **Do not proceed with the work** — you do not own the issue. Instead, go back to step 1: call `act_next` (MCP) or `act ready` + `act update --claim` (CLI) on a different unblocked issue. Working a contended issue produces orphaned commits and is never recoverable via re-try — the winner is authoritative.

2. Do the work. Write tests. Run them.
3. **Commit with the issue marker.** The marker is the `Act-Id: act-XXXXXX` trailer that goes in the commit BODY (the two `-m` flags produce a body paragraph separated from the subject by a blank line, matching `git interpret-trailers` form). The trailer enables `act doctor` orphan-close detection.
   - MCP: the `commit_marker` field on the `act_next` response is the ready-to-use string.
   - CLI: `act show <id> --commit-marker` prints a single line, e.g. `Act-Id: act-25aae7`.
   - **Don't construct the marker by hand** — slicing the id directly is how the marker drifts out of `act doctor`'s grep window. The trailer form is invisible to conventional-commit linters, survives squash-merge cleanly, and is the only emission shape going forward; doctor still resolves the historical `(act-XXXX)` subject-line form for back-compat in pre-migration repos.

   Example commit command (CLI):
   ```
   git commit -a -m "<subject>" -m "Act-Id: act-XXXXXX"
   ```

4. **Review the diff** — see the Review step section.
5. **Close the issue and push.**
   - MCP: `act_finish` — closes the issue and pushes in one call.
   - CLI: `act close <id> --reason "<one-liner>"` then `git push origin main` (or whatever the project's default branch is) — concurrent agents see the close immediately; session-death can't lose work.
6. Repeat from step 1 until `act ready` (or `act_next`) returns empty.

## Review step (step 4)

Orchestrator-judged based on the change's scope and risk:

- **No review:** typo fixes, doc touch-ups, formatting-only commits, comments. Trust the work + your own checks; close. *Examples: fixing a help string typo, updating a CHANGELOG entry, correcting an inline comment.*
- **Lightweight review (default for ergonomic features and bugfixes):** `feature-dev:code-reviewer` over the diff with `>70% confidence` filter. Goal is signal not nits. Findings → file as follow-up issues, fix the load-bearing ones inline, close on the rest. *Examples: adding a new CLI flag, fixing an off-by-one in a display formatter, adding a missing error message, small UX improvements.*
- **Multi-modal review (default for changes affecting agent workflow, public API, or concurrency semantics):** code-reviewer + a UX/walkthrough reviewer + (where appropriate) a real workflow run by a fresh agent. These catch non-overlapping defect classes; relying on one leaves blind spots. *Examples: changing claim/close locking semantics, modifying the op-log append path, altering `act state export` merge logic, adding a new public subcommand, changing the canonical-loop skill guidance.*
- **Pre-implementation review (for big architectural moves):** review the plan before writing code. Cheaper to throw away an approach than a refactor. *Examples: introducing a new data persistence layer, designing a breaking change to the issue-id schema, adding a streaming coordination plane, changing the public MCP tool contract.*

Reviewer prompts **must**: (a) pin the commit hash explicitly, (b) include the changed file paths, (c) require the reviewer to state "I read commit X at paths Y" as the first line of output before any findings, (d) set a confidence floor (>70% is the validated default from the aac-website dogfood), and (e) ask for a "what's working well" closing section.

The "must" on (c) is load-bearing. A reviewer that can't actually read the diff produces confidence numbers calibrated against nothing — they are speculative findings dressed up as analysis. Real damage from the aac-website dogfood (act-a9d0): the reviewer couldn't read the worktree blobs and confidently flagged concerns at 80%+ that were already handled by the actual code. Confidence ≠ accuracy when the reviewer is reasoning from absence of evidence. Review findings without a confirmed-read are not findings; they are guesses.

File the review itself as an act issue (claim, run, close-with-derivative-pointers) — same audit lifecycle as feature work.

When to skip: you genuinely have to. Don't skip just because the change is small; small changes have introduced load-bearing bugs (the act repo's `act-act-` double-prefix bug passed every test). When in doubt, lightweight review.

## Halt conditions

The work loop is autonomous by default. Halt and surface only when the question requires context the agent literally doesn't have:

- **Spec ambiguity:** acceptance criteria conflict, are missing a load-bearing detail, or two reasonable interpretations would produce visibly different code.
- **Breaking change:** a fix can't be made strictly additive — existing callers would have to change. Human decides whether to take the breakage or design around it.
- **Cross-issue scope:** the right fix needs another currently-open issue's fix to land first. File the dep and surface; do not silently expand scope.
- **Deeper defect:** tests for the current issue reveal a bug bigger than the issue's description. File a follow-up; decide whether the current issue still makes sense to land standalone, surface if not.
- **External obligation:** anything cross-repo or genuinely public-facing — publishing a release, pushing a tag, opening a PR against another repo, sending notifications. *Pushing same-branch commits to origin is part of the loop, not an obligation to halt on.*

Implementation choices, push cadence, branch hygiene, when to run reviews, isolation choices, retry-vs-halt, follow-up-vs-fix-now — these are orchestrator calls. **Document the decisions you make** (in commit messages, status summaries) so the human can course-correct, but don't escalate them.

## Mid-flight discoveries

Bugs and surface gaps you hit *while working a different issue* go straight into the backlog as follow-ups; they do **not** halt the current task:

```
act create "<title>" --type bug \
    --description "<repro + when discovered>" \
    --accept "<resolution criterion>"
```

Pattern: file it, keep working. If the discovery actually blocks the current issue, that's the "cross-issue scope" halt condition; halt.

**Backlog-check before any `act create`.** Whether the prospective new issue comes from a mid-flight discovery, an external list (TODO file, audit doc, retrospective findings), or a delegated subagent's task — grep the existing backlog first. Use `act list --search '<keywords>'` or `act ready` and confirm the issue isn't already tracked under a different title before filing. Discovered during the aac-website dogfood: a docs-triage subagent translated three to-do-list.md items into duplicates of existing seed issues (act-a4b6/a744/0578 dup'd act-4141/8a44/218d). The claim a finding is new requires evidence it isn't already tracked.

## External dependencies

When an issue is blocked on work in a sibling tracker act doesn't import — a Linear ticket, a GitHub issue in another repo, a Jira card — attach an opaque ref:

```
act update <id> --ext-add "linear:ENG-123"
act update <id> --ext-add "gh:org/other-repo#42"
```

The ref is stored verbatim; act doesn't interpret it. An issue with at least one external dep is excluded from `act ready` the same way an unresolved internal block excludes. The caller owns the lifecycle — when the upstream work is done, clear the ref:

```
act update <id> --ext-rm "linear:ENG-123"
```

Both add and remove are idempotent (re-adding a present ref is a no-op at the apply layer; removing an absent ref succeeds silently). In MCP sessions, use the `ext_add` and `ext_rm` arrays on `act_update`.

Use `--ext-add` / `--ext-rm` for cross-tracker blocks; use `act dep add` for act-to-act block edges. The two compose: an issue may carry both, and either kind keeps it out of `act ready` until cleared.

## Sub-agents

Whether to spawn sub-agents is a harness decision, not an act rule. The key principle is **write-scope isolation**: each sub-agent must own a disjoint slice of the filesystem so that concurrent `git commit` and act ops don't collide.

**Claude Code harness — `isolation: "worktree"`.** Two un-isolated agents in the same working tree collide on the git index even when their files don't overlap — `act update --claim` fails fast on a dirty index. Worktrees give each agent its own working directory and branch. When you spawn a worktree agent, **override the loop in their prompt** so they push to their *worktree branch* (not main). An integrator (you, or another agent) merges the branch in once their work returns clean. Before dispatching any worktree subagent, read `references/worktree-subagents.md` — it covers the `--push` trap and parallelism-vs-isolation guidance that has caused real damage.

**Codex harness — `spawn_agent`.** Codex provides container-level isolation: each `spawn_agent` call runs in its own sandbox with its own writable root. The same disjoint-scope principle applies — each agent should own non-overlapping work. The act canonical loop runs identically inside the container; `act state import` is still the correct seeding mechanism when the orchestrator pre-provisions the `.act/` state.

**Parallelism vs isolation (harness-neutral).** Isolation (worktree or container) removes the git-index collision problem; it does not make conflicting work safe. Default serial when issues touch overlapping files regardless of harness. Spawn parallel only when issues are provably disjoint.

## Commit discipline

- Every work commit includes the issue's `Act-Id: act-XXXXXX` trailer in the commit body (separated from the subject by a blank line). Use two `-m` flags or a heredoc; do not append the marker to the subject line. Doctor still resolves the historical `(act-XXXX)` subject-line form for back-compat, but new commits emit only the trailer.
- Group `act create` / `act update` / `act dep add` ops with their work commit when they're load-bearing for the issue (e.g. filing follow-ups for an unresolved acceptance criterion). Otherwise let the auto-commit per `act` op stand on its own.
- Use `--no-commit` only for true bootstrap or migration cases where bundling is the right unit.

## Documentation discipline

Cold-start agents (no conversation history, just the docs + the codebase) execute documentation literally — they don't paraphrase, smooth, or interpolate gaps. If a doc in this project claims a behavior, that claim must have an asserting test exercising the behavior at the user-visible boundary, not at internal-state level.

Real consequence from the act repo: the original `(act-XXXX)` commit-marker bug passed every internal test (assertions on op-file bytes) but produced wrong git log subjects (assertions on commit message strings would have caught it). When you add a doc claim, add the test that asserts it. The marker form has since switched to the `Act-Id: act-XXXXXX` trailer (act-c4c5); the lesson stands at every shape.

## Pre-close gates

If the project has `.act/hooks/close` (an executable script), it runs before every close commit and aborts the close on non-zero exit. This is where projects put the language-specific lint/format/test gates that should run before any "I'm done with this issue" claim — e.g. Go projects run `gofmt -l`, `go vet`, `go test ./...`; TS projects run `tsc --noEmit && eslint && vitest`. The hook receives the op envelope on `$ACT_HOOK_OP_JSON` and runs in the project's repo root.

When you start work in a new project, look for `.act/hooks/close`. If it exists, every close runs through it. If it doesn't and the project has a CI suite, consider creating one — it makes "broke CI on push" failures locally-detectable before they fan out across multiple close commits.

## Working in a worktree or sandbox

**Before claiming any issue:** run `git branch` and confirm the current branch matches the worktree-branch named in your dispatch prompt. If they differ, do not proceed — surface the mismatch to the orchestrator. This check catches the case where your worktree was set up against an unexpected ref; claiming and committing on the wrong branch entangles your work with another agent's.

**Push scope:** push only to your own worktree branch (`git push origin HEAD:<your-worktree-branch>`). Never push to `main`, `master`, or any branch you didn't create for this dispatch. Scope-guarding push prevents your commits from bypassing the integrator's merge gate and colliding with concurrent agents or open PRs.

If you've been dispatched as a sub-agent into a git worktree (or any other isolated sandbox) by an orchestrator, `act` works normally in your environment because the orchestrator pre-seeds a `.act/` copy via `act state import <your-path>` before launching you. The seeded `.act/` mirrors the orchestrator's view of the backlog at dispatch time — you can call `act ready`, `act update --claim`, `act create`, `act close`, etc. exactly as you would in the main checkout.

**If your directory was created during dispatch and has no `.act/` yet, seed it yourself with `act state import --from-cwd <orchestrator-path>`** (run from inside your directory; target defaults to your cwd). Never `cp -r <orchestrator>/.act .` by hand: that copies the orchestrator's live `index.db`, which the orchestrator may have open, and the resulting stale/locked index causes silent op-write loss — your `act create` returns success but the op never lands where the orchestrator's `act state export` can see it. The `--from-cwd` mode copies only the op log + config and rebuilds the index locally, which is always correct.

**You do NOT need to coordinate with main during execution.** No mid-flight pushes, no syncing, no checking whether another worker has landed work. Run the canonical loop locally. The orchestrator runs `act state export <your-path>` against your directory at teardown (or at failure-cleanup) to copy any ops you wrote back into the main `.act/` and commit them there. Export is one-way append and idempotent, so the orchestrator's `act create` calls during your run, your `act close`, and any follow-ups you filed all make it back — even if the headline task fails.

**Staleness window.** Your view of the tracker is frozen at dispatch time. If another worker dispatched in the same pass files an issue or closes one mid-flight, you won't see it; cross-worker visibility is at-merge-back, not real-time. This is acceptable for the Phase 1.5 dispatcher model — workers are typically scoped to disjoint units of work and the orchestrator sequences shape-changing work into single-unit waves. Phase 2 closes the staleness window with a streaming coordination plane; see `docs/coordination-plane-phase2-design.md` once it lands in your project.

### Phase 2 dispatch (push-attached)

When the orchestrator's `.act/.git/config` has `act.role=orchestrator` and an `origin-upstream` is wired (one-time per project via `act remote enable`, then optionally `act remote add-upstream <url>`), the dispatch flow shifts from copy-based seeding to push-attached seeding. Workers are seeded by `act state import --from-remote <url> <path>` and push their ops directly to the orchestrator's `.act/.git` during execution. The orchestrator's post-receive hook re-folds on every push and replicates to the configured upstream — no per-cycle export is needed for normal flows. The staleness window narrows from at-merge-back to next-push.

`act state export` remains the fallback for sandboxed workers without network access to the orchestrator, and as a failure-mode rescue when push-attached delivery breaks mid-run. Same shape as Phase 1.5: ops are append-only and the merge is idempotent, so a late export after a partial push is safe.

Cross-reference: see `docs/migration-runbook.md` "Phase 1.5 → Phase 2 cutover" for the one-time operator setup and the rollback path.

## Codex sandbox approvals

Codex agents run inside a sandbox with restricted network and filesystem access. Several canonical-loop operations may require explicit approval from the operator, or may be blocked entirely depending on the sandbox policy:

**`git push`** — the push step (loop step 5 / `act_finish`) reaches the network. If the sandbox has no outbound network access, push will hang or fail. Two options:
- If the orchestrator pre-grants network access to the origin host, push proceeds normally.
- If not, omit the push from the agent's loop (`--offline` flag on write commands), accumulate commits locally, and let the integrator push after inspecting the branch. This is the same trade-off as the Phase 1.5 state-export pattern: ops are committed locally and exported at teardown.

**Merge flows** — operations that merge a worktree branch back to main (`git merge --ff-only`) need write access to the host working tree. In Codex, each `spawn_agent` runs in its own container; the integrator (the calling agent or a separate merge pass) performs the merge in a container with write access to the appropriate path.

**Operations outside the writable root** — Codex restricts writes to a declared writable root (usually the repo checkout). Act's nested `.act/` repo falls inside the project root and is within the writable root by default. If `.act/` is mounted separately or lives at a non-standard path, confirm it's within the declared writable root before running act commands.

**Approval checklist for Codex dispatch:**
1. Confirm `.act/` is within the container's writable root.
2. Decide whether `git push` is in-loop (network-enabled sandbox) or deferred to integrator (offline mode).
3. If using `spawn_agent` for sub-agents, note that each container gets its own writable filesystem — `act state import` still applies for `.act/` state seeding.

**Claude Code note:** Claude Code's analogous approvals are documented in `references/setup.md` (the `permissions.allow` list for `git push`, `git merge --ff-only`, etc.). If you're on Claude Code, that file is the right reference; this section is for Codex operators.

## Worktree dispatch and orchestrator pattern

**This pattern is Claude Code-specific.** The Claude Code harness supports `isolation: "worktree"` in agent dispatch, which creates a git worktree for each sub-agent. The canonical orchestrate loop (`/orchestrate` skill, `act state export`) is built around this shape: the orchestrator dispatches N worktree agents, each runs the canonical loop on its own branch, and the orchestrator exports + merges at completion.

**Codex equivalent:** Codex doesn't have a worktree concept natively — isolation comes from `spawn_agent` containers. The same coordination model applies: each `spawn_agent` runs the canonical loop (claim → work → close → optionally push), and the top-level agent integrates after all sub-agents complete. The act op-log works the same way inside each container; the `act state export` step replaces any cross-container seeding that would be needed in a shared filesystem model.

**For project-level orchestration config** (dispatch prompts, state-export wiring, the `/orchestrate` skill itself): those live in the project's `CLAUDE.md` or `AGENTS.md`, not here. The skill documents the per-agent loop; orchestrator wiring is project-specific.

## Per-project overrides

Project-specific rules live in the repo's `CLAUDE.md` (or `AGENTS.md`). That file is for deviations and rationale, not duplication of this skill. If a rule belongs in every project that uses act, it lives here; if it belongs in just one project, it lives in that project's `CLAUDE.md`.
