---
title: "Follow-up: flag parser stops at first positional, dropping spec-form flags"
deps: [act-65e6]
acceptance_criteria:
  - "`act create \"title\" -p 1 --json` (flags AFTER positional) parses identically to `act create -p 1 --json \"title\"` (flags BEFORE positional)."
  - "All commands that take both flags and positionals (`create`, `show`, `close`, `dep add`, `update`) accept interleaved flag ordering."
  - "Smoke test in `internal/cli/smoke_test.go` (or a new test) exercises the spec-literal `act create \"title\" -p 1 --json` calling convention and asserts JSON output."
status: open
created_at: 2026-04-29T12:00:00Z
---

# Follow-up: flag parser stops at first positional

## Context
Stage 8 verification (docs/verification.md) noted that Go's stdlib `flag.FlagSet` stops parsing at the first non-flag argument. The spec test plan and §Commands documentation use the form `act create "title" -p 1 --json`, but with the current parser the flags after the positional are silently dropped — `act create` runs without `--json` and emits human output.

## Resolution options
1. Migrate `cmd/act` to `spf13/pflag` or another POSIX-compliant parser that supports interleaving.
2. Manually rearrange argv before passing to `flag.FlagSet.Parse` (parse twice, or split flags/positionals up front).

Option 1 is cleaner; option 2 avoids a new dependency. Pick one.

## Scope
- Apply the chosen approach to all subcommand wrappers in `cmd/act/`.
- Add a smoke test that exercises spec-literal flag ordering.
- Update README if any docs example shows the wrong ordering.
