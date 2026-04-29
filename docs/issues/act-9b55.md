---
title: "Golden tests for fold determinism"
deps: [act-9362, act-c9f0, act-a1f6]
acceptance_criteria:
  - "One golden directory per (op_type, op_version) pair under testdata/golden/<op_type>/<version>/<case>.json"
  - "Each case has prior_state.json, op.json, expected_state.json"
  - "Test loads prior, applies op, compares expected via byte-equal canonical JSON"
  - "Required cases present: create, update-status, update-priority, update-assignee, update-description, update-accept, claim, close, dep-add, dep-rm, redact, migrate, compact"
  - "Adding a new op_version requires adding a new golden directory; CI fails if the directory is missing"
  - "A coverage test enumerates the (op_type, op_version) registry from act-c9f0 and fails when any pair lacks a golden directory"
  - "Update mode: a flag (e.g., -update) regenerates expected_state.json files when intentional changes occur"
status: closed
created_at: 2026-04-29T00:00:00Z
---

# Golden tests for fold determinism

## Context
Implements spec-v2 §7.2. Golden tests are the readable record of fold semantics: every `(op_type, op_version)` pair has at least one golden case showing prior state + op → expected state. They lock the apply functions against accidental change and document the contract for new contributors.

## Scope
- Directory structure `testdata/golden/<op_type>/<version>/<case>.json`, where each case is a directory containing `prior_state.json`, `op.json`, `expected_state.json`.
- Test loader walks the tree, applies each op via the fold dispatcher, compares against expected via canonical JSON byte-equality.
- Required cases (one each, more allowed): `create`, `update-status`, `update-priority`, `update-assignee`, `update-description`, `update-accept`, `claim`, `close`, `dep-add`, `dep-rm`, `redact`, `migrate`, `compact`.
- Coverage test: enumerate the registry of `(op_type, op_version)` from act-c9f0 and assert every pair has a golden directory.
- Update mode: `-update` flag (or env var) rewrites expected_state.json files in place when the fold output changes intentionally; reviewer inspects diff in PR.

## Out of scope
- Property/fuzz tests (act-a64e).
- Concurrency tests (act-0c76).
- Golden tests for command-level output (this issue is for fold; CLI golden output may come later).

## Implementation notes
- All three JSON files are stored canonicalized so the test diff is meaningful and round-trip stable.
- The applier uses the same registry the production fold uses; no test-only dispatch.
- `migrate` and `compact` cases rely on infra from act-5af9 and act-a0ad respectively; the golden tests stub the writer-version registry as needed.
- `redact` cases must include a prior op-history snippet so the redact's index references resolve (per §5.A.2 redact indices refer to post-fold current index space at the redact's HLC).
- The coverage test reads the registry at build time so adding a new `(op_type, op_version)` without a golden dir is a hard CI failure rather than a silent gap.

## Test plan
- Cite spec §7.2.
- Smoke test: load and run all goldens; assert zero diffs on a clean tree.
- Coverage test: temporarily remove a known golden directory, assert the coverage test fails with a clear missing-directory message.
- Update-mode test: introduce a deliberate state-shape change, run `-update`, assert files regenerate; assert plain run then passes.
- Deterministic JSON: assert `expected_state.json` bytes are stable byte-for-byte across runs (no map-iteration randomness leaking through).
