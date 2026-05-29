# docs/

This directory holds the tracked reference material for `act`. The list below
is the complete set of tracked docs, each labeled **authoritative** (a
public-facing reference a contributor or user reads to understand or use the
project) or **archive** (a historical record kept under a clear label because it
still has reference value).

Process artifacts — brief iterations, spec-review rounds, plan-review passes,
point-in-time audit/status/verification reports, the dispatcher prompt, and the
pre-nested-repo issue markdown — are **not** tracked here. They were part of how
the project was built, not part of what a contributor consumes. They live in git
history and (for live work) in the `act` tracker under `.act/`.

## Specification

| Doc | Label | What it is |
|-----|-------|------------|
| [spec-v2.md](spec-v2.md) | authoritative | The canonical v1 specification — data model, command surface, op-fold/concurrency, errors, hooks, migration. The single implementable reference; cited directly by the doc-claim sweep and several `TestDocClaim_*` tests. |
| [spec.md](spec.md) | authoritative | Predecessor full spec. Retained because doc-claim tests assert invariants (e.g. the NTFS-safe op-filename format) against it alongside spec-v2. |
| [spec-section-commands.md](spec-section-commands.md) | authoritative | Spec section: the CLI command and MCP tool surface, universal flags, exit codes, JSON schemas. |
| [spec-section-data-model.md](spec-section-data-model.md) | authoritative | Spec section: issue/op data model and on-disk layout. Cited by the op-filename doc-claim test. |
| [spec-section-fold.md](spec-section-fold.md) | authoritative | Spec section: HLC, op-fold algorithm, per-field merge, atomic claim, determinism contract. |
| [spec-section-tests.md](spec-section-tests.md) | authoritative | Spec section: error taxonomy, hook contract, bootstrap, migration, compaction, test plan. |

## Design and architecture

| Doc | Label | What it is |
|-----|-------|------------|
| [coordination-plane-design.md](coordination-plane-design.md) | authoritative | The coordination-plane design (nested `.act/` git repo, marker placement, multi-writer model). Cited by code and by the doc-claim sweep. |
| [coordination-plane-phase2-design.md](coordination-plane-phase2-design.md) | authoritative | Phase 2 design brief — `act remote sync`, harvest/bootstrap, doctor divergence checks. |
| [coordination-plane-phase2-plan.md](coordination-plane-phase2-plan.md) | authoritative | Phase 2 implementation plan (sequenced, scoped tickets). Cited by name in tracked source comments (`harvest.go`, `remote.go`, `bootstrap_worker.go`, `gitops.go`) as provenance for the Phase 2 surface. |
| [commit-noise-design.md](commit-noise-design.md) | authoritative | Design note quantifying the commit cost of an issue's lifecycle; the rationale behind the commit-noise reduction work. |
| [orchestration-design.md](orchestration-design.md) | authoritative | Planning notes for `act` + orchestration (dispatcher pattern, agent fan-out). |
| [distribution-options-brief.md](distribution-options-brief.md) | authoritative | Brief on secondary distribution options (brew tap vs curl installer). |
| [migration-runbook.md](migration-runbook.md) | authoritative | Operator runbook for the nested-repo migration. Cited by the doc-claim sweep and migration tests. |

## Archive

| Doc | Label | What it is |
|-----|-------|------------|
| [act-evaluation.md](act-evaluation.md) | archive | A read on whether `act` is worth using for agent-driven work, drawn from the dogfood loop and code review. |
| [aac-website-dogfood-debrief.md](aac-website-dogfood-debrief.md) | archive | Debrief from dogfooding `act` on the aac-website backlog — where the skill creaked and what was learned. |
| [dogfood/phase2-real-use-2026-05-19.md](dogfood/phase2-real-use-2026-05-19.md) | archive | Phase 2 real-use validation record (the canonical-arc dogfood step). |
| [issues/act-2e8d.md](issues/act-2e8d.md) | archive | Pre-nested-repo issue note for the CI-matrix workflow. Retained because it is cited by name in `internal/cli/smoke_test.go`. |
| [issues/act-e1d4.md](issues/act-e1d4.md) | archive | Pre-nested-repo issue note for the `act ready` algorithm. Retained because it is cited by name in `internal/cli/ready.go`. |
