# AGENTS.md — working on act

Conventions for anyone (agent or human) working **on the `act` codebase itself** — how
to develop, test, and reason about changes here. This is build-side; it is *not* how to
*use* `act` as a task tracker (that lives in the `act` skill — `act install-skill`, then
`act help`). Written for a contributor with no prior context.

## Where things live (doc boundaries)

act's docs split across surfaces, and content drifts into the wrong one without explicit
boundaries. Place content by these rules, and move it when you find it misfiled:

- **The `act` skill (`skills/act/SKILL.md`)** — how to *use* act: the claim/work/close
  loop, commit-marker discipline, external blockers, pre-close gates. Not anything
  *above* act — worker dispatch, worktree/sandbox isolation, code-review — which
  describe a workflow that *uses* act and belong to whatever drives it, not to act.
- **This file (`AGENTS.md`)** — build-side code-facts and rationale: how to develop and
  test act, the documentation-discipline rule. Not *operating policy* (push cadence,
  when to review, default isolation mode) — that depends on the agent setup and lives
  with the operator's workflow, not a contributor guide.
- **`README.md`** — what act is and how to install it, for a new adopter.

## Project specifics

- **The binary.** The plugin's shipped artifacts are **committed** under `bin/`: a `uname`
  launcher `bin/act` plus one binary per arch (`bin/act-<os>-<arch>`). They are produced
  and committed by the release CI, not built locally: `release.yml` (a `workflow_dispatch`
  with a `version`) calls the kit's reusable workflow, which cross-compiles every arch,
  version-stamps each (`-ldflags -X github.com/aac/act/internal/version.Binary=<version>`),
  ad-hoc-signs the darwin binaries, and commits the result to the default branch — that
  commit is the release (commit-to-main model; no tags, no Release assets). They must be
  committed because a `"source": "./"` plugin install ships only tracked files and
  `/plugin install` never fetches release assets. For local dev, build to a throwaway
  path — `go build -o bin/act-dev ./cmd/act` (reports version `dev`) — and don't clobber
  the committed launcher.
- **This repo dogfoods act on its own backlog.** Mid-flight discoveries about act itself
  are common; file them as `type=bug` and keep working — that's the dogfood signal.
- **Most churn is in `cmd/act/` and `internal/cli/`** (argparser, error envelope,
  command dispatcher) — useful orientation for where changes tend to land.
- **MCP dev override:** the tracked `.mcp.json` runs `${CLAUDE_PLUGIN_ROOT}/bin/act` (the
  committed launcher); Codex uses `.codex-plugin/mcp.json` (`./bin/act`, cwd `.`) since it
  can't expand `${CLAUDE_PLUGIN_ROOT}`. To develop against a local build, don't edit them —
  load a gitignored `.mcp.local.json` pointing at `./bin/act-dev` with
  `claude --mcp-config .mcp.local.json`.

## Documentation discipline

**The most important engineering rule in the repo.** Two past drift bugs shipped past
green test suites that asserted on internal state instead of the user-visible surface.

**Rule.** Every user-visible behavior claim made in a doc requires an asserting test
that exercises the claimed behavior at the user-visible boundary. The claim and the test
land in the same commit; the claim is not "shipped" until the assertion exists.

A claim is **user-visible** when it surfaces to an agent or human reading the project
cold: a subcommand `--help`/flag-help string, any text inside `act help`, README
behavior or example invocations, this file or the `act` skill (when the claim is about
act's behavior), or a spec invariant that callers are expected to honor.

**Naming convention.** Tests asserting a user-visible doc claim are named
`TestDocClaim_*` and live beside the package whose surface they exercise. A sweep test
(`internal/cli/docs_sweep_test.go`) holds a registry of `(doc, claim, test)` tuples and
fails if a registered claim is missing its assertion or vice-versa. When you add a
user-visible claim, append a tuple and write the matching test in the same commit; run
`go test ./...` before close to catch orphans.

**Why this, not "more tests".** Both prior bugs had thorough internal tests that passed
while the user-visible behavior was broken — the command bailed before it ever reached
the asserted code path. Asserting at the boundary the doc names is what catches that
class of bug.

## Design posture

`act` favors clean redesign over backward-compat shims, and halts on breaking changes
rather than carrying compat for a single caller's convenience. The rationale for
specific mechanisms — prefix resolution, the `Act-Id:` commit-body trailer, the
nested-`.act/` repo layout — lives in code comments next to each mechanism, not here.
Conventions for *running* agents against act (push cadence, when to review, isolation
mode, halt-vs-proceed) are operating policy, not facts about the code; they live with
your workflow.

## Relationship to the act skill

The canonical *workflow* for any project using act (claim, work, close, push) lives in
the `act` skill, embedded in the binary and installed with `act install-skill`. This
file holds only conventions for developing act itself; when a rule here proves general
enough to apply to any act-using project, it gets promoted into the skill.
