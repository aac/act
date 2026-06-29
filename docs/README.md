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
| [spec.md](spec.md) | authoritative | The canonical v1 specification — data model, command surface, op-fold/concurrency, errors, hooks, migration. The single implementable reference; cited directly by the doc-claim sweep and several `TestDocClaim_*` tests. |

## Design and architecture

| Doc | Label | What it is |
|-----|-------|------------|
| [coordination-plane-design.md](coordination-plane-design.md) | authoritative | The coordination-plane design (nested `.act/` git repo, marker placement, multi-writer model). Cited by code and by the doc-claim sweep. |
