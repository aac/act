---
title: "CI matrix for three target environments"
deps: [act-9cad, act-a64e, act-9b55, act-0c76]
acceptance_criteria:
  - "Three CI jobs run on every PR: CC laptop, CC on the Web, Cowork"
  - "CC laptop: Linux container approximating Claude Code on a laptop; runs install + smoke workflow (init, create, claim, close, list --json) and asserts JSON shapes via jq schema checks"
  - "CC on the Web: container matching the web sandbox; runs the same smoke workflow"
  - "Cowork: container with the Cowork plugin manifest; drives act mcp over stdio and runs the MCP E2E from §7.5"
  - "All three jobs must pass for the PR to be eligible to merge"
  - "Each job posts a single PASS/FAIL summary to the run for an automated agent to read"
  - "Property tests, golden tests, fuzz corpus replay, and concurrency tests all execute in at least one job (typically CC laptop)"
  - "Pipeline stages: lint → unit/property/golden → fuzz-corpus-replay → concurrency → smoke (matrix)"
status: open
created_at: 2026-04-29T00:00:00Z
---

# CI matrix for three target environments

## Context
Implements spec-v2 §7.8. The act binary must work identically across the three target sandboxes the team uses today: Claude Code on a laptop, Claude Code on the web, and Cowork. CI proves the contract on every PR; an automated agent reads the PASS/FAIL summary and decides whether the build pipeline advances. Humans only sign off on the final tag.

## Scope
- Three GitHub Actions (or equivalent) jobs, each pinned to a container image:
  - `cc-laptop`: Linux container approximating CC on a laptop.
  - `cc-web`: container matching the web sandbox.
  - `cowork`: container with the Cowork plugin manifest installed.
- Pipeline stages, executed in order with fail-fast off (so all three matrix jobs report independently):
  1. lint (gofmt, go vet, golangci-lint)
  2. unit + property (act-a64e) + golden (act-9b55)
  3. fuzz corpus replay (act-a64e) — replay only, not a fresh fuzz run
  4. concurrency (act-0c76)
  5. smoke matrix (this issue) — the three target-environment jobs
- Smoke workflow per job: `act init`, `act create`, claim via `act update --claim`, `act close`, `act list --json`. JSON shape asserted via `jq` schema checks (or equivalent jq-based assertions).
- Cowork job additionally drives `act mcp` over stdio and runs the MCP E2E from §7.5.
- PASS/FAIL summary posted as a single-line GitHub check or comment that an agent can grep.

## Out of scope
- Fresh fuzzing run in CI (corpus replay only; new fuzz happens on a scheduled job, not per-PR).
- Cross-platform builds (Windows/macOS) — covered by act-64af.
- Release pipeline / artifact upload (act-64af).

## Implementation notes
- Container images are versioned and cached; image digests are pinned in the workflow file so reproducibility holds across PRs.
- The `cowork` job uses the published Cowork plugin manifest; the manifest reference is pinned by digest.
- The PR-merge gate: the human-set branch protection rule requires all three matrix jobs green; the agent-readable PASS/FAIL summary is in addition to the standard required-checks gate.
- Each smoke step asserts both exit code and JSON shape; missing keys, extra keys, and type mismatches all fail the job.
- The pipeline steps share a workspace; later stages reuse the build artifacts produced by lint/unit so the binary is built exactly once per job.

## Test plan
- Cite spec §7.8.
- Self-test the workflow: a no-op PR runs the full pipeline and all three smoke jobs go green within the budget.
- Failure injection: introduce a deliberate JSON-shape regression in `act list --json`; assert each smoke job fails with a clear jq-assertion error.
- Cowork-specific: assert the MCP E2E from §7.5 runs in the cowork job and contributes its own PASS/FAIL line.
