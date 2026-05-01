---
title: "act create --blocked-by and composed act_file_blocker MCP tool"
deps: []
acceptance_criteria:
  - "`act create <title> --blocked-by <id> [--blocked-by <id>...]` accepts one or more dep targets and writes the `create` + `dep-add` ops in a single auto-commit."
  - "On any failure after create succeeds, the partial state is rolled back (op file unstaged) so the bug never exists with no edge."
  - "MCP composed tool `act_file_blocker` accepts `{title, blocked_by, ...create-flags}` and is one tool call."
  - "`act_file_blocker` flips the *blocking* issue (the one identified by `blocked_by`) to `status=blocked` if and only if a `--block-parent` flag is set; default is to file-and-link without modifying the parent."
  - "Surface-gap-analysis Workflows A and C are reduced from 2 calls to 1."
status: open
milestone: v0.2
created_at: 2026-04-29T13:00:00Z
---

# act create --blocked-by and composed act_file_blocker MCP tool

## Context

Workflows A (file a bug after a failed test) and C (file a follow-up while implementing X) both require: create issue, add `blocks` edge. Today this is two non-atomic calls. If the second fails, an orphaned bug exists with no edge to its blocker. The fix is a composed primitive.

This shape parallels the existing `act_block` (status=blocked + dep-add); `act_file_blocker` is the create-side analog. The CLI `--blocked-by` flag is the same primitive without the MCP wrapper.

## Severity

Annoying — every bug-filing flow pays this tax.
