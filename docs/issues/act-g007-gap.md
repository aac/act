---
title: "act_next output should include commit_marker string"
deps: []
acceptance_criteria:
  - "`act_next` JSON output adds a `commit_marker` field of the form `(act-<short>)` using the same shortest-unique prefix logic the CLI already computes."
  - "`act_next` description / docstring instructs callers to include `commit_marker` in their work-commit messages so `act doctor orphan-close` can correlate."
  - "An equivalent `act show <id> --commit-marker` flag emits just the marker string for non-MCP callers."
  - "Doctor's orphan-close check is unchanged; this is purely an ergonomic surface."
status: open
milestone: v0.2
created_at: 2026-04-29T13:00:00Z
---

# act_next output should include commit_marker string

## Context

The doctor `orphan-close` check looks for `(act-<prefix>)` in commit messages to correlate work commits with closed issues. Today, `act_next` returns `id` and `prefix` but not a ready-to-paste `(act-<prefix>)` string. The agent must construct it, with two ways to get it wrong (forget parens, forget prefix).

Returning `commit_marker` directly removes the construction step and the failure modes.

## Severity

Annoying — small but every-claim friction.
