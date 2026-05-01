---
title: "act log --summary one-line-per-op timeline view"
deps: []
acceptance_criteria:
  - "`act log <id> --summary [--json]` emits one row per op with `(hlc, op_type, node_id, brief)` where `brief` is a 1-line synopsis of the payload."
  - "Synopsis is deterministic per op_type (e.g. `update_field` → `field=<name> value=<short>`; `claim` → `assignee=<name>`; `close` → `reason=<short>`)."
  - "Output is stable enough that `act doctor`-style tooling can scrape it; payload truncation cap is 80 chars."
  - "Without `--summary`, today's full output is unchanged."
status: open
milestone: v0.2
created_at: 2026-04-29T13:00:00Z
---

# act log --summary one-line-per-op timeline view

## Context

Workflow D (audit a closed issue) wants a quick chronological read of who did what when. Today's `act log` emits the full op envelope per op — load-bearing for forensics but verbose for the common case of "show me the timeline."

A `--summary` mode that emits one line per op is a small affordance with high readability payoff.

## Severity

Nice-to-have — reduces friction on a workflow that already works.
