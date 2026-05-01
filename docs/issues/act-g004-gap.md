---
title: "act mine and act ready --mine for self-scoped queries"
deps: []
acceptance_criteria:
  - "`act mine [--json]` lists issues where assignee == current node's effective identity (resolved from `.act/config.json` user.email or an explicit `--as` flag) AND status in {in_progress, blocked}."
  - "`act ready --mine` filters the ready queue to issues already assigned to the current identity."
  - "Both commands document the identity-resolution rule explicitly so agents can predict behavior across machines."
  - "Output schema matches `act list` for `act mine` and `act ready` for `act ready --mine` (no new shape)."
status: open
milestone: v0.2
created_at: 2026-04-29T13:00:00Z
---

# act mine and act ready --mine for self-scoped queries

## Context

An agent in the middle of a task has no one-call way to ask "what am I working on?" or "what's next for me specifically?" It must construct `act list --assignee=<self> --status=in_progress` and know its own assignee string. This is doable but requires the agent to thread its identity through every call.

`act mine` and `act ready --mine` are convenience wrappers that resolve identity once.

## Severity

Annoying — frequent enough to matter; not blocking.
