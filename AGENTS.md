# AGENTS.md — working on act

Conventions and rationale for agents (and humans) working **on the `act` codebase itself**. This is build-side: it tells you how to develop, test, and reason about changes to this repo. It is not how to *use* `act` as a task tracker — that lives in the `act` skill (`act install-skill`, then `~/.claude/skills/act/SKILL.md`) and in `act help`.

## Project specifics

- **The binary** is at `./bin/act` (gitignored). Rebuild with `go build -o bin/act ./cmd/act` if missing.
- **This repo dogfoods act on its own backlog.** Mid-flight discoveries about act itself are commonplace; file them as `type=bug` and keep working — that's the dogfood signal we want.
- **Default to serial sub-agents in this repo.** Most outstanding work touches `cmd/act/` and `internal/cli/` (argparser, error envelope, command dispatcher), so parallel agents tend to merge-conflict at integration time even with worktrees. Override only when issues are provably disjoint.

## Documentation discipline

**This is the most important engineering rule in the repo.** Two past drift bugs shipped past green test suites that asserted on internal state instead of the user-visible surface, so:

**Rule.** Every user-visible behavior claim made in a doc requires an asserting test that exercises the claimed behavior at the user-visible boundary. Adding the claim and the test is the same commit; the claim is not "shipped" until the assertion exists.

A claim is **user-visible** when any of the following surfaces it to an agent or a human reading the project cold:
- A subcommand `--help` line or flag-help string (`fs.String(... "...")`).
- Any text inside `act help` (`helpOverview`, `helpWorkflow`, `helpOpsModel`, `helpErrors` in `cmd/act/help.go`).
- README sections that show example invocations or describe behavior.
- This file (`AGENTS.md`) or the `act` skill when the rule is about act's behavior, not its workflow conventions.
- `docs/spec-v2.md` invariants that callers (CLI, MCP, tests) are expected to honor.

**What counts (with examples):**
- "Prefix ok" on `--under <id>` → a test driving `act ready --under <unique-prefix>` and asserting it resolves (not "the resolver returned the right id").
- The canonical-loop step "6. git push" in `act help` → a test asserting `act help` output contains `git push` in the loop section.
- The commit-marker format claimed in `act help workflow` and the spec → a test reading `git log -1 --format=%B` for the `Act-Id:` trailer (not the op-file envelope).
- The "claim is atomic; concurrent claimers resolve last-write-wins" claim in README → a test driving two sequential `act update --claim --isolated` subprocesses with different node_ids against the same issue, asserting the first (earlier HLC) wins (exit 0, `claimed:true`) and the second loses (exit 5, `claimed:false`, `error:claim_lost`) at the subprocess boundary. Sequential invocation is equivalent to truly concurrent for fold winner-selection, which depends only on HLC ordering, not subprocess launch order.

**What does NOT count** (still write tests, but not under this rule):
- An internal helper's invariant that isn't documented anywhere user-visible (e.g. `ops.foldEnvelopes` returns the latest by HLC). Test it, but it isn't a doc claim.
- A package-private function's behavior. Internal unit tests are the right home.
- A planned/aspirational behavior in a design doc that hasn't shipped yet.

**Naming convention.** Tests that assert a user-visible doc claim are named `TestDocClaim_*` and live alongside the package whose surface they exercise (`internal/cli/docclaim_test.go`, `cmd/act/docclaim_test.go`). A sweep test (`internal/cli/docs_sweep_test.go`) holds a registry of `(doc, claim, required_TestDocClaim_*)` tuples and fails if a registered claim is missing its assertion or vice-versa. When you add a new user-visible claim, append a tuple to the registry and write the matching test in the same commit. Run `go test ./...` before close to catch the orphan.

**Why this discipline, not "more tests".** Both prior drift bugs had thorough internal tests. The prefix-ok bug had a passing test that asserted `ResolvePrefix` returned the right candidate when given a 2-char prefix — but the CLI command bailed before reaching the resolver because of an unrelated length check. The internal test passed; the user-visible behavior was broken. Asserting at the boundary the doc names is what catches that class of bug.

## Versioning rationale

Design decisions made for this repo, each with the discovery that prompted it. `act` is pre-v1 with a small, controlled user base, so these favor clean redesign over backward-compat shims.

- *Halt on breaking changes.* `act` is pre-v1; there's still freedom to redesign cleanly. Better to surface the question once than carry compat shims for a single user's convenience.
- *Push after every close, not at session end.* Matches the dispatcher pattern, makes closes visible to concurrent agents immediately, and means a dropped session never silently swallows finished work. Verbose git history is the accepted cost. Discovered when an early dogfood agent followed the original loop, didn't push, and left commits local-only.
- *Sub-agents must use `isolation: worktree` by default.* Un-isolated agents collide on the git index even with disjoint files because `git commit` serializes per working tree. Op-log file-level concurrency only helps when each writer has its own working tree. Discovered when a second sub-agent blocked the parent session from claiming a different issue in the same tree.
- *Prefix resolution accepts any non-empty hex prefix, not just ≥4 chars.* Every doc and help string says "prefix ok" for id arguments. The `MinShortHexLen=4` floor governs display and id generation; it no longer applies to user-supplied lookup. `ids.MinInputHexLen=1` is the floor for resolution. An empty hex tail (bare `act-` or whitespace) still returns not_found. This lets agents use e.g. `act show act-c2` when unique. Error-envelope distinction: `issue_not_found` (code `issue_not_found`, no candidates, exit 3) vs `id_ambiguous` (code `id_ambiguous`, `details.candidates[]` lists all matching full ids sorted, capped at `MaxAmbiguousCandidates=16`, exit 2 per the universal table).
- *Review step in the loop, with orchestrator-judged scope.* The canonical loop has a review step (see the `act` skill). Lessons from the first overall review: (1) a confidence filter at >70% gives high-signal findings instead of taste-level noise — keep this default; (2) pin the commit ref explicitly in reviewer prompts; (3) ask for a "what's working well" closing section so subsequent work knows what NOT to break; (4) reviews are first-class tracked tasks in act, with derivative-issues-on-close as the audit pattern.
- *`per_session` bundle_strategy retired under Phase 1.* The original mechanism (close stages into the agent's work commit when the working tree has non-`.act` changes) was retired when Phase 1 moved act op-commits into a nested `.act/` git repo. The bundle-into-host-work-commit story relied on the close commit being in the *same* repo as the agent's code change; once those live in different repos (host vs nested `.act/`) the staging dance no longer makes sense. Under Phase 1, every close commits standalone in the nested repo. The `bundle_strategy` config field, the `CloseResult.staged_for_commit` flag, and the staging branch in `RunClose` were all removed; the `Act-Id:` trailer form on the host work commit is unchanged because the agent's separate host-repo work commit still carries it for orphan-close correlation.
- *Deferred work goes in its own ticket, not a sibling's close-reason.* A close-reason once said work was "deferred to <other ticket> per design suggestion"; that other ticket closed clean and the migration never happened — only surfaced when a later run hit "no act state" from inside `.act/`. Three mechanism failures stacked: (1) no dep edge wired between predecessor and inheritor, so a reader of the inheritor saw nothing about the deferred work; (2) the inheritor's accept criteria never enumerated the migration, so closing "all accepts met" was technically correct; (3) the predecessor's own accept criterion sounded boundary-level but the test exercised the internal API, not the CLI surface — the same documentation-discipline drift the rule above guards against. Concrete rule: when you defer work out of a ticket's scope, file the follow-up as its own ticket with a `relates`/`derives-from` dep edge to the predecessor — never park it in a close-reason and name a sibling as inheritor.
- *Work-commit marker is a body trailer, not a subject suffix.* Work commit messages embed `Act-Id: act-XXXXXX` as a trailer in the commit body, not as a subject-line suffix. The trailer form is the only emission shape going forward — no config knob, no per-repo opt-in. Doctor's grep matches both the trailer form and the historical subject-line form so pre-migration history resolves cleanly. Rationale: trailers survive squash-merge intact, are invisible to conventional-commit linters (commitlint, husky), are ignored by semantic-release CHANGELOG generators, and are easy for external contributors to ignore — the prerequisites for Phase 1's "outside contributors see exactly the code" goal (`docs/coordination-plane-design.md`, "Marker placement"). The `act-` short id width is 6 hex chars; doctor's trailer regex accepts `Act-Id: act-[0-9a-f]{4,}` to support both pre-migration 4-char ids and current 6-char ids.

## Relationship to the act skill

The canonical *workflow* for any project using act (how to claim, work, close, push) lives in the `act` skill, bundled into the binary and installed with `act install-skill`. This file holds only the conventions and rationale specific to developing act itself. When a rule here proves general enough to apply to any act-using project, it gets promoted into the skill; the entries above are the ones that are project-specific or still evolving.
