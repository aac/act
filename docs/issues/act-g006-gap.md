---
title: "--description-file flag for act create and act update"
deps: []
acceptance_criteria:
  - "`act create <title> --description-file <path>` reads the file's contents (UTF-8) as the description payload."
  - "`act update <id> --description-file <path>` ditto."
  - "`-` is accepted as a sentinel for stdin."
  - "Mutually exclusive with `--description`; using both is exit 2."
  - "File size cap matches the schema's 16384-char description limit; oversize is exit 2 with a clear error."
status: open
milestone: v0.2
created_at: 2026-04-29T13:00:00Z
---

# --description-file flag for act create and act update

## Context

Workflow A (file a bug after a failed test run) wants to attach the failing test's stderr tail to the bug's description. Multi-line text via shell-escaped `--description "..."` is fragile across shells, especially in CI. A `--description-file <path>` (or `-` for stdin) flag is a one-line ergonomic fix.

## Severity

Annoying — comes up every time an agent has structured output to attach.
