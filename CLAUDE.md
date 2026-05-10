# CLAUDE.md — act repo

This repo dogfoods `act` on its own backlog. Agents are the primary user; this file is the agent-runtime layer on top of `act help`.

## First session, every session

1. `./bin/act help` if you've never used `act` before. Mechanics live there; this file is for opinions and project-specific rules. If `bin/act` is missing, `go build -o bin/act ./cmd/act`. For the JSON error-envelope contract (shape, code list, byte-counted length rule), see `./bin/act help errors`.
2. `./bin/act ready` to see the unblocked frontier. Pick the highest-priority issue.
3. `./bin/act update --claim <id>` to take it.
4. Do the work, write tests, run them.
5. `git commit -m "<summary> (<short-id>)"` — the marker enables `act doctor` orphan-close detection.
6. **Review the diff** — see "Review step" below. Decide what kind, file follow-ups for findings.
7. `./bin/act close <id> --reason "<one-liner>"`.
8. `git push origin main` — concurrent agents see the close immediately; session-death can't lose work.
9. Repeat from step 2 until `act ready` is empty.

In an MCP-equipped session, prefer `act_next` + `act_finish` over the raw CLI; `.mcp.json` wires this up automatically.

## Review step

Step 6 of the loop is "review the diff." The orchestrator decides which kind based on the change's scope and risk. The standard cuts:

- **No review:** typo fixes, doc touch-ups, formatting-only commits, comments. Trust the work + your own checks; close.
- **Lightweight review (default for ergonomic features and bugfixes):** a `feature-dev:code-reviewer` sub-agent over the diff with `>70% confidence` filter. Goal is signal not nits. Findings → file as follow-up issues, fix the load-bearing ones inline, close on the rest.
- **Multi-modal review (default for changes affecting agent workflow, public API, or concurrency semantics):** code-reviewer + a UX/walkthrough reviewer + (where appropriate) a real workflow run by a fresh agent. These catch non-overlapping defect classes; rely on one and you'll miss things the others would have caught.
- **Pre-implementation review (for big architectural moves):** review the *plan* before writing code. Cheaper to throw away an approach than a refactor.

Reviewer prompts should always: pin the commit hash explicitly, set a confidence floor, and ask for a "what's working well" section at the end so subsequent work knows what *not* to break. File the review itself as an act issue (claim, run, close-with-derivative-pointers) — same audit lifecycle as feature work.

When to skip the review step: you genuinely have to. Don't skip just because the change is small; small changes have introduced load-bearing bugs in this repo (act-d3a5's `act-act-` double prefix had passed every test). When in doubt, lightweight review.

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

Whether to spawn sub-agents is a harness decision, not an `act` rule. For this repo specifically:

**Default: `isolation: "worktree"`.** Two un-isolated agents in the same working tree collide on git index even when their files don't overlap — `act update --claim` fails with `error: Please commit or stash them` while another agent has any uncommitted writes anywhere in the tree. Worktrees give each agent its own working directory and branch.

When you spawn a worktree agent, override step 7 of the loop in their prompt: they push to their *worktree branch* (not main), and an integrator (you, or another agent) merges the branch in once their work returns clean. Step 7 in this file is for the main-tree work.

The only safe exception to worktree-isolation is when you can guarantee no other writer will touch the tree until the spawn finishes — i.e. you're not also working at the same time. Otherwise: worktree.

Parallelism is a separate concern. Even with worktrees, default serial when issues touch overlapping files (most v0.2 work touches `cmd/act/` and `internal/cli/` — argparser, error envelope, command dispatcher); merge time costs more than parallel time saves. Spawn parallel only when issues are provably disjoint (e.g. an MCP-only change next to a CLI-only change with no overlapping files).

## What this file is not

Not a tutorial — `./bin/act help` is the tutorial. Not an architecture doc — `docs/spec-v2.md` and the brief are. This file captures *recommendations and rules for working in this repo* that don't fit elsewhere. After 3–5 sessions of real use, stable patterns extract into a global skill triggered by the presence of `.act/`.

## Versioning rationale

Each rule below is versioned so a later skill-extraction pass can decide what's load-bearing. New rules should be added inline with their rationale.

- *Halt on breaking changes* (2026-05): `act` is pre-v1; we still have freedom to redesign cleanly. Better to surface the question once than carry compat shims for a single user's convenience.
- *File mid-flight bugs as follow-ups, don't halt* (2026-05): the dogfood signal is the bug landing in the backlog with a clear repro, not a half-finished current task.
- *Default serial sub-agents in this repo* (2026-05): v0.2 ergonomic issues all touch CLI code. Parallelism would cost more in merge time than it saves.
- *Push after every close, not at session end* (2026-05): matches the dispatcher pattern, makes closes visible to concurrent agents immediately, and means a dropped session never silently swallows finished work. Verbose git history is the accepted cost. Discovered when the first dogfood agent (act-6bbd) followed the original loop and didn't push, leaving 3 commits local-only — see act-ac52.
- *Sub-agents must use isolation:worktree by default* (2026-05): un-isolated agents collide on git index even with disjoint files because `git commit` serializes per working tree. Op-log file-level concurrency (the multi-writer thesis from the brief) only saves you when each writer has its own working tree. Discovered when sub-agent #2 on act-5467 blocked the parent session from claiming a different issue in the same tree — see act-6e2b.
- *Prefix resolution accepts any non-empty hex prefix, not just ≥4 chars* (2026-05): every doc and help string says "prefix ok" for id arguments (act-6fca). The MinShortHexLen=4 floor governs display and id generation; it no longer applies to user-supplied lookup. `ids.MinInputHexLen=1` is the floor for resolution. An empty hex tail (bare "act-" or whitespace) still returns not_found. This lets agents use e.g. `act show act-c2` when unique. Error-envelope distinction: `issue_not_found` (code `issue_not_found`, no candidates, exit 3) vs `id_ambiguous` (code `id_ambiguous`, `details.candidates[]` lists all matching full ids sorted, capped at `MaxAmbiguousCandidates=16`, exit 2 per the universal table — see act-8dcd).
- *Review step in the loop, with orchestrator-judged scope* (2026-05): the canonical loop now has step 6 ("review the diff"); see the Review step section. Lessons from the first overall review (act-da03): (1) confidence filter at >70% gave high-signal findings instead of taste-level noise — keep this default; (2) pin the commit ref explicitly in reviewer prompts (the first review's intro line cited a stale hash); (3) ask for a "what's working well" section at the end so subsequent work knows what NOT to break — guidance for future authors as much as a reviewer politeness; (4) reviews are first-class tracked tasks in act, with derivative-issues-on-close as the audit pattern.
