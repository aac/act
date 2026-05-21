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
- *act-a659 / per_session bundle_strategy retired under Phase 1* (2026-05, act-7410cb): the original mechanism (close stages into the agent's work commit when the working tree has non-`.act` changes; per_session bundle_strategy in `.act/config.json` controlled the deferral) was retired when Phase 1 moved act op-commits into a nested `.act/` git repo. The bundle-into-host-work-commit story relied on the close commit being in the *same* repo as the agent's code change; once those live in different repos (host vs nested `.act/`) the staging dance no longer makes sense. Under Phase 1, every close commits standalone in the nested repo. The `bundle_strategy` config field, the `CloseResult.staged_for_commit` flag, and the staging branch in `RunClose` were all removed (act-7410cb); doctor's `Act-Id: act-XXXXXX` trailer form (act-c4c5) is unchanged because the agent's separate host-repo work commit still carries it for orphan-close correlation.
- *Deferred work goes in its own ticket, not a sibling's close-reason* (2026-05, act-0852da precedent): The closed-reason on `act-9e8c` said "cmd/act findRepoRoot deferred to act-37f7 per design suggestion." `act-37f7` closed clean and the migration never happened — only surfaced when the third-session financial-repo run hit "no act state" from inside `.act/`. Three mechanism failures stacked: (1) no dep edge wired between the predecessor and the inheritor, so a reader of the inheritor saw nothing about the deferred work; (2) the inheritor's accept criteria never enumerated the migration, so closing "all accepts met" was technically correct; (3) the predecessor's own accept #4 ("Resolver runs cleanly from each of: …") sounded boundary-level but the test exercised the internal API, not the CLI surface — the same documentation-discipline drift that bit act-6fca / act-ac52. Concrete rule from this instance: when you defer work out of a ticket's scope, file the follow-up as its own ticket with a `relates`/`derives-from` dep edge to the predecessor — never park it in a close-reason and name a sibling as inheritor. Not yet promoted to the global skill (one instance is thin); promote if this recurs. The pattern is already exemplified correctly by `act-2e1070` (deferred subcommand for `act-00e5cc`), filed in the same session this precedent was discovered.
- *Work-commit marker is a body trailer, not a subject suffix* (2026-05, act-c4c5): work commit messages embed `Act-Id: act-XXXXXX` as a trailer in the commit body, not `(act-XXXX)` as a subject-line suffix. The trailer form is the only emission shape going forward — no config knob, no per-repo opt-in. Doctor's grep matches both the new trailer form and the historical subject-line form so pre-migration history resolves cleanly. Rationale: trailers survive squash-merge intact, are invisible to conventional-commit linters (commitlint, husky), are ignored by semantic-release CHANGELOG generators, and are easy for external contributors to ignore — the prerequisites for Phase 1's "outside contributors see exactly the code" goal (docs/coordination-plane-design.md v2.1 "Marker placement"). The `act-` short id width itself was widened to 6 hex chars in act-f9a0; doctor's trailer regex accepts `Act-Id: act-[0-9a-f]{4,}` to support both pre-migration 4-char ids and new 6-char ids.

## Documentation discipline

The global skill says it once at a high level; this section is the operational rule for *this* repo because both prior drift bugs (`act-6fca`, `act-ac52`) shipped past green test suites that asserted on internal state instead of the user-visible surface.

**Rule.** Every user-visible behavior claim made in a doc requires an asserting test that exercises the claimed behavior at the user-visible boundary. Adding the claim and the test is the same commit; the claim is not "shipped" until the assertion exists.

A claim is **user-visible** when any of the following surfaces it to an agent or a human reading the project cold:
- A subcommand `--help` line or flag-help string (`fs.String(... "...")`).
- Any text inside `act help` (`helpOverview`, `helpWorkflow`, `helpOpsModel`, `helpErrors` in `cmd/act/help.go`).
- README sections that show example invocations or describe behavior.
- `CLAUDE.md` (this file) or the global skill at `~/.claude/skills/act/SKILL.md` when the rule is about act's behavior, not its workflow conventions.
- `docs/spec-v2.md` invariants that callers (CLI, MCP, tests) are expected to honor.

**What counts (with examples):**
- "Prefix ok" on `--under <id>` → a test driving `act ready --under <unique-prefix>` and asserting it resolves (not "the resolver returned the right id").
- The canonical-loop step "6. git push" in `act help` → a test asserting `act help` output contains `git push` in the loop section.
- The commit-marker format `(act-XXXX)` claimed in `act help workflow` and the spec → a test reading `git log -1 --format=%s` (not the op-file envelope).
- The "claim is atomic; concurrent claimers resolve last-write-wins" claim in README → a concurrent test driving two parallel claims and asserting only one wins, the other gets `claim_lost`.

**What does NOT count** (still write tests, but not under this rule):
- An internal helper's invariant that isn't documented anywhere user-visible (e.g. `ops.foldEnvelopes` returns the latest by HLC). Test it, but it isn't a doc claim.
- A package-private function's behavior. Internal unit tests are the right home.
- A planned/aspirational behavior in a design doc that hasn't shipped yet.

**Naming convention.** Tests that assert a user-visible doc claim are named `TestDocClaim_*` and live alongside the package whose surface they exercise (`internal/cli/docclaim_test.go`, `cmd/act/docclaim_test.go`). A sweep test (`internal/cli/docs_sweep_test.go`) holds a registry of `(doc, claim, required_TestDocClaim_*)` tuples and fails if a registered claim is missing its assertion or vice-versa. When you add a new user-visible claim, append a tuple to the registry and write the matching test in the same commit. Run `go test ./...` before close to catch the orphan.

**Why this discipline, not "more tests".** Both prior drift bugs had thorough internal tests. The prefix-ok bug had a passing test that asserted `ResolvePrefix` returned the right candidate when given a 2-char prefix — but the CLI command bailed before reaching the resolver because of an unrelated length check. The internal test passed; the user-visible behavior was broken. Asserting at the boundary the doc names is what catches that class of bug.

## Promotion log

When a rule here graduates to the global skill, leave a one-liner in this section so the history is preserved:

- 2026-05-10: Initial skill extraction. Canonical loop, halt conditions, sub-agent isolation, mid-flight discovery pattern, commit discipline, review step, and documentation discipline all promoted to `~/.claude/skills/act/SKILL.md`. This file slimmed to project-specific overrides + rationale archive.
- 2026-05-17: Documentation discipline elaborated locally (act-ff5c). The global skill says it; this repo carries the operational rule, the `TestDocClaim_*` naming convention, and the sweep registry because it's where the drift bugs landed. Promote to the skill if a second act-using project develops the same surface area.
