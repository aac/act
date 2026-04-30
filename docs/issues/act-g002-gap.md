---
title: "act reopen <id> CLI command"
deps: []
acceptance_criteria:
  - "`act reopen <id> [--reason TEXT] [--json]` writes a `reopen` op as defined in spec §5.B.4."
  - "On success, `closed_at` and `closed_reason` are cleared per the spec's reset-last_hlc semantics."
  - "Status is set to `open` (not `in_progress`); a follow-up `--claim` is needed to take the issue."
  - "Subsequent `act show` reflects the reopened state."
  - "`act update --status open` on a closed issue continues to be rejected (spec §5.A.4); the test suite asserts both behaviors."
  - "Standard universal flags apply (`--no-commit`, `--push`, `--isolated`, `--verify`)."
status: open
created_at: 2026-04-29T13:00:00Z
---

# act reopen <id> CLI command

## Context

Spec §5.B.4 defines the `reopen` op type and its precise semantics: clear `closed_at`, clear `closed_reason`, reset their `last_hlc`. Spec §5.A.4 explicitly rejects `act update --status open` against a closed issue. There is no CLI verb that emits a `reopen` op.

Net effect: an agent with a regressed bug has no way to reopen the original issue from the supported surface. Filing a duplicate breaks the audit chain (`supersedes` edge can hide the link but doesn't restore the original).

## Severity

Critical — a documented op type with no CLI driver.
