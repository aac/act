---
title: "act delete <id> CLI command (tombstone op)"
deps: []
acceptance_criteria:
  - "`act delete <id> [--reason TEXT] [--json]` writes a `tombstone` op as defined in the spec's op-type table."
  - "After delete, subsequent reads of the issue return only the tombstone marker per spec line 378."
  - "Doctor `dangling-deps` continues to flag deps pointing at tombstoned ids."
  - "Standard universal flags apply."
  - "The command refuses to delete an issue with non-tombstoned descendants unless `--cascade` is set; `--cascade` walks the parent edge and tombstones each."
status: open
created_at: 2026-04-29T13:00:00Z
---

# act delete <id> CLI command (tombstone op)

## Context

`tombstone` (issue-level deletion) is a documented op type with no CLI verb. Spec line 378 specifies the post-tombstone read semantics. An agent that needs to remove an erroneously-created issue has no supported path.

## Severity

Annoying — rare, but documented op with no driver is a surface hole.
