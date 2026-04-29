---
title: "Doctor checks"
deps: [act-912f, act-9824, act-296e, act-5ca9]
acceptance_criteria:
  - "act doctor runs all 8 checks: orphan-close, orphan-ops, dangling-deps, time-travel, cycle, unknown-op-version, index-divergence, index-schema"
  - "--check NAME is repeatable; default runs every check"
  - "--fix replaces .act/index.db on index-divergence and drops/rebuilds on index-schema"
  - "--fix is read-only for the other six checks"
  - "unknown-op-version always exits 4 even with --fix"
  - "exit 0 on all-pass; exit 1 if any check fails and --fix did not remediate; exit 2 on bad flags"
  - "JSON output shape matches spec: {ok, checks:[{name,status,findings,fixed}], summary:{pass,fail}}"
  - "--compact flag triggers manual compaction of eligible issues"
status: closed
created_at: 2026-04-29T00:00:00Z
closed_at: 2026-04-29T00:00:00Z
---

# Doctor checks

## Context
Implements `act doctor` per spec-v2 §"act doctor" and §6 (edge cases for unknown-op-version). The doctor is the consistency oracle: it walks the on-disk op log and the SQLite index, surfaces drift, and offers safe auto-remediation for the two index-related findings.

## Scope
- CLI command `act doctor [--check NAME] [--fix] [--json] [--compact]`.
- Eight checks, each implemented as an isolated function returning `(findings []Finding, fixable bool)`:
  - `orphan-close`: issues with `closed_at` set but no `(act-<prefix>)` marker in the closing commit AND no diff under `.act/ops/<id>/` at close time.
  - `orphan-ops`: op files referencing an `issue_id` with no `create` op.
  - `dangling-deps`: dep edges (post-resolution) pointing at unknown ids.
  - `time-travel`: adjacent ops with HLC going backward more than the 5-minute drift bound.
  - `cycle`: cycle in the `blocks` subgraph.
  - `unknown-op-version`: any op with `writer_version > self.writer_version`. Cannot fix; exits 4.
  - `index-divergence`: recompute index into a tmp SQLite from ops/snapshots; row-by-row diff against `.act/index.db` (recomputed = oracle). `--fix` replaces `.act/index.db` with recomputed db.
  - `index-schema`: compare index schema version to expected. `--fix` drops and rebuilds `.act/index.db`.
- JSON output and exit-code policy from spec.
- `--compact` triggers manual compaction (delegates to act-a0ad).

## Out of scope
- Compaction algorithm itself (act-a0ad).
- Rebuild logic for the index (act-912f provides it; doctor calls it).
- Fixing orphan-close, orphan-ops, dangling-deps, time-travel, cycle: read-only by design.

## Implementation notes
- Run order is fixed and deterministic so JSON output is stable across runs.
- Each finding is `{code, kind, details, fixed:bool}`. `kind` is `"warn"` or `"fail"`.
- The recomputed-oracle SQLite for `index-divergence` uses an in-memory or `:memory:`-backed copy and is destroyed after diff unless `--fix` swaps it onto disk via atomic rename.
- `unknown-op-version` short-circuits the run: when found, exit 4 immediately without attempting other index-mutating fixes (avoid writing data with a possibly-stale binary).
- Findings emitted by `unknown-op-version` correlate with squash-and-push refusal (spec §6) so operators can locate offending commits before pushing.

## Test plan
- Spec §7.6: positive and negative test per check. Synthesize the broken state, assert exactly one finding with the expected code; from a clean seeded repo, assert zero findings.
- `--fix` round-trip: corrupt the index, run `act doctor --check index-divergence --fix`, assert exit 0 and that a re-run reports zero findings.
- `--fix` on `unknown-op-version`: assert exit 4 unchanged.
- JSON shape conformance test against the spec sample.
