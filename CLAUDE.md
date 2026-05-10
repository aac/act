# CLAUDE.md — act repo

The canonical workflow for any project using act lives in the global `act` skill at `~/.claude/skills/act/SKILL.md`. This file holds **only** the deviations and rationale specific to the act repo itself.

## Project specifics

- **The binary** is at `./bin/act` (gitignored). Rebuild with `go build -o bin/act ./cmd/act` if missing.
- **This repo dogfoods act on its own backlog.** Mid-flight discoveries about act itself are commonplace; file them as type=bug and keep working — that's the dogfood signal we want.
- **Default serial sub-agents in this repo.** Most outstanding work touches `cmd/act/` and `internal/cli/` (argparser, error envelope, command dispatcher) so parallel agents tend to merge-conflict at integration time even with worktrees. Override only when issues are provably disjoint.

## Versioning rationale

Decisions made for this repo, with the discovery that prompted each. Mostly these have been promoted to the global skill; the entries here are the ones too project-specific to live there or that document a still-evolving call.

- *Halt on breaking changes* (2026-05): `act` is pre-v1; we still have freedom to redesign cleanly. Better to surface the question once than carry compat shims for a single user's convenience.
- *Push after every close, not at session end* (2026-05): matches the dispatcher pattern, makes closes visible to concurrent agents immediately, and means a dropped session never silently swallows finished work. Verbose git history is the accepted cost. Discovered when the first dogfood agent (act-6bbd) followed the original loop and didn't push, leaving 3 commits local-only — see act-ac52.
- *Sub-agents must use isolation:worktree by default* (2026-05): un-isolated agents collide on git index even with disjoint files because `git commit` serializes per working tree. Op-log file-level concurrency (the multi-writer thesis from the brief) only saves you when each writer has its own working tree. Discovered when sub-agent #2 on act-5467 blocked the parent session from claiming a different issue in the same tree — see act-6e2b.
- *Prefix resolution accepts any non-empty hex prefix, not just ≥4 chars* (2026-05): every doc and help string says "prefix ok" for id arguments (act-6fca). The MinShortHexLen=4 floor governs display and id generation; it no longer applies to user-supplied lookup. `ids.MinInputHexLen=1` is the floor for resolution. An empty hex tail (bare "act-" or whitespace) still returns not_found. This lets agents use e.g. `act show act-c2` when unique. Error-envelope distinction: `issue_not_found` (code `issue_not_found`, no candidates, exit 3) vs `id_ambiguous` (code `id_ambiguous`, `details.candidates[]` lists all matching full ids sorted, capped at `MaxAmbiguousCandidates=16`, exit 2 per the universal table — see act-8dcd).
- *Review step in the loop, with orchestrator-judged scope* (2026-05): the canonical loop has a review step (see global skill). Lessons from the first overall review (act-da03): (1) confidence filter at >70% gave high-signal findings instead of taste-level noise — keep this default; (2) pin the commit ref explicitly in reviewer prompts (the first review's intro line cited a stale hash); (3) ask for a "what's working well" closing section so subsequent work knows what NOT to break; (4) reviews are first-class tracked tasks in act, with derivative-issues-on-close as the audit pattern.

## Promotion log

When a rule here graduates to the global skill, leave a one-liner in this section so the history is preserved:

- 2026-05-10: Initial skill extraction. Canonical loop, halt conditions, sub-agent isolation, mid-flight discovery pattern, commit discipline, review step, and documentation discipline all promoted to `~/.claude/skills/act/SKILL.md`. This file slimmed to project-specific overrides + rationale archive.
