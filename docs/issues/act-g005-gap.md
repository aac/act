---
title: "act dep add direction aliases (--blocks, --blocked-by)"
deps: []
acceptance_criteria:
  - "`act dep add <a> --blocked-by <b>` is equivalent to today's `act dep add <a> <b> --type blocks` (a is blocked by b)."
  - "`act dep add <a> --blocks <b>` is equivalent to today's `act dep add <b> <a> --type blocks` (a blocks b)."
  - "Today's positional form continues to work unchanged."
  - "Help text and docs use 'blocked-by' / 'blocks' rather than 'parent' / 'child' to avoid collision with the `issue.parent` hierarchy field."
  - "Direction is unambiguous from the flag name; no agent needs to consult docs to file the right edge."
status: open
created_at: 2026-04-29T13:00:00Z
---

# act dep add direction aliases (--blocks, --blocked-by)

## Context

Today's `act dep add <child> <parent> --type blocks` overloads the term "parent." `issue.parent` is the hierarchy edge; `dep.parent` is the blocking edge. Agents must read the spec each time to confirm direction. Adding directional flag aliases (`--blocks`, `--blocked-by`) eliminates the ambiguity.

## Severity

Annoying — easy to file the wrong direction; quietly corrupts the ready queue.
