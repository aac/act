---
title: "act show should surface closer identity for audit"
deps: []
acceptance_criteria:
  - "`act show <id> --json` on a closed issue includes `closed_by_node` (the node_id of the writer that emitted the close op)."
  - "`act show <id> --json` optionally also includes `closed_by_tree` (the git tree hash already computed and stored at close time per spec line 681)."
  - "Folded snapshot adds these fields without breaking the existing JSON schema; existing consumers ignore unknown fields."
  - "Doctor `orphan-close` continues to use the same evidentiary chain; this change is read-only on the snapshot path."
  - "Audit walkthrough (Workflow D in surface-gap-analysis.md) is satisfied without invoking `act log`."
status: open
created_at: 2026-04-29T13:00:00Z
---

# act show should surface closer identity for audit

## Context

Surface-gap-analysis Workflow D found that the most basic audit question — "who closed this issue?" — requires dropping from `act show` to `act log` and grepping for the close op's `node_id`. The folded snapshot has `assignee` (last value, can drift after close) but no authoritative closer field. This is a load-bearing miss for an explicitly-supported audit flow.

The fold engine already computes `closed_at` and `closed_reason` from the close op; capturing the same op's `node_id` into a `closed_by_node` field is mechanical and free.

`closed_by_tree` is already computed and persisted in the SQLite index per spec; surfacing it through `act show` makes deeper forensics (which commit was associated with the close?) cheap.

## Severity

Critical — blocks the audit workflow without a workaround other than log-grep.
