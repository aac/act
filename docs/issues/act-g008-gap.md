---
title: "act redact CLI command"
deps: []
acceptance_criteria:
  - "`act redact <id> --field <field-path> [--value TEXT] [--json]` writes a `redact` op per spec §5.A.2 and the op-payload schema."
  - "`<field-path>` accepts the indexed forms documented in the spec (e.g. `description`, `acceptance_criteria[2].text`)."
  - "Idempotent per spec edge case (line 1042): re-redacting the same field returns `{changed: false}` and exit 0."
  - "Standard universal flags apply (`--no-commit`, `--push`, `--isolated`, `--verify`)."
  - "Doctor `orphan-ops` continues to recognize redact ops correctly."
status: open
created_at: 2026-04-29T13:00:00Z
---

# act redact CLI command

## Context

`redact` is a documented op type with detailed semantics (spec §5.A.2, edge case at line 1042). There is no CLI verb. Agents needing to redact accidentally-leaked content from an issue's description or acceptance criteria have no supported path.

The op exists for a reason — the brief calls out redact as the supported way to remove content while preserving the immutability invariant — but the surface is unreachable without writing an op file by hand.

## Severity

Annoying — low frequency, but high impact when needed (secret leakage incident response).
