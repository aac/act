---
title: "Follow-up: act close does not update live SQLite index"
deps: [act-bdc8, act-912f]
acceptance_criteria:
  - "After a normal `create → update --claim → close` sequence in a fresh repo, `act doctor --json` reports zero findings."
  - "The close op invokes `index.Upsert` (or equivalent) so the live SQLite index reflects status=closed without requiring a doctor --fix rebuild."
  - "A regression test in `internal/cli/close_test.go` (or an integration test) folds the issue, queries the index, and asserts current row matches rebuilt row."
status: open
created_at: 2026-04-29T12:00:00Z
---

# Follow-up: act close does not update live SQLite index

## Context
Stage 8 verification (docs/verification.md) discovered that after `act create → act update --claim → act close`, `act doctor --check index-divergence` reports an `error` severity finding. The current SQLite row shows `status=in_progress` while a rebuilt index from the op log correctly shows `status=closed`. The op is durable on disk; only the live cache fails to update.

## Likely root cause
`internal/cli/close.go::RunClose` calls `WriteOpAndAutoCommit` to persist the op + commit, but does not invoke `internal/index.Index.Upsert` for the post-close issue state. Most other write commands have the same issue but it isn't surfaced because they update fields the doctor doesn't pivot on.

## Scope
- Update `internal/cli/close.go` to refold the issue and `index.Upsert` after a successful op write.
- Audit `internal/cli/{create,update,depadd}.go` for the same pattern; apply consistent index updates.
- Doctor's `index-divergence` check should pass cleanly after every supported write command in the standard happy path.
