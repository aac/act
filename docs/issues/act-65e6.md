---
title: "act create command"
deps: [act-bd70, act-3bbe, act-5ca9, act-912f, act-ce9f]
acceptance_criteria:
  - "`act create <title>` builds a `create` op payload, derives `act-<16hex>` from `sha256(payload || nonce)`, and writes the op file to `.act/ops/<id>/<yyyy-mm>/<iso>-<hash6>-create.json`."
  - "Repeated `--accept` flags append in order; `--priority`, `--parent`, `--type`, `--description` populate the payload; `--parent` is resolved through the id-resolution pipeline."
  - "Hash collisions retry the nonce up to 8 times; on persistent collision exit 1."
  - "JSON output: `{\"ok\": true, \"id\": \"act-<16hex>\", \"prefix\": \"act-<short>\", \"op_id\": \"<hex>\", \"committed\": <bool>, \"pushed\": <bool>}` plus `warnings: [\"parent_closed\"]` per §5.C.4 when `--parent` resolves to a closed issue."
  - "`post-create` hook runs after the op file is written and before the op-commit; non-zero hook exit returns command exit 1."
  - "Universal write flags (`--no-commit`, `--push`, `--isolated`, `--verify`) are honored; conflict combinations exit 2."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# act create command

## Context
Spec §3 `act create <title>` (lines 558–581). Composes the canonical create op (§1.5 op-type payloads), derives the issue id (§1.4), writes per §1.7 file naming, runs hooks (§4.2), and op-commits (§4 auto-commit). Closed-parent warning per §5.C.4.

## Scope
- Argument parsing: positional `<title>`; flags `-p/--priority`, `--parent`, `--accept` (repeatable), `--type`, `--description`, `--json`, plus universal write flags.
- Validate title (non-empty, ≤256 bytes), priority (0..3), type enum (task|bug|epic|chore).
- Resolve `--parent` via id-resolution pipeline; allow closed/redacted parents with a warning.
- Build payload, generate nonce, hash → issue id (16 hex, `act-` prefix).
- Write op file at `.act/ops/<id>/<yyyy-mm>/<iso>-<hash6>-create.json`.
- Run `post-create` hook; surface failure as exit 1.
- Auto-commit unless `--no-commit`; honor `--push`, `--isolated`, `--verify`.

## Out of scope
- Editing existing issues (handled by `act update`).
- Dependency graph mutation beyond `--parent` (use `act dep add`).
- Index population (folded automatically; not part of this command's IO).

## Implementation notes
- Flags: `-p/--priority` int 0..3 default 2; `--parent` resolved; `--accept` repeatable; `--type` enum default `task`; `--description` default `""`; `--json` default false (true under MCP).
- Exit codes: `0`; `1` hook reject or 8 nonce retries exhausted; `2` bad flags, unknown parent (resolution exit 2 ambiguous), title violations (empty or >256 bytes); `3` `.act/` missing or parent not found per §5.C.1; `4` writer-version skew during implicit fold.
- JSON output: `{"ok": true, "id": "act-<16hex>", "prefix": "act-<short>", "op_id": "<hex>", "committed": <bool>, "pushed": <bool>}`. With `--json` and a closed parent: include `"warnings": ["parent_closed"]` and suppress stderr text per §5.C.4.
- Side effects: one op file written, optional hook execution, optional git commit + push.
- Universal flag precedence per §3 universal flags: `--no-commit + --push` exit 2; `--isolated + --push` exit 2.

## Test plan
- Happy path: `act create "fix bug" -p 1 --type bug --accept "tests pass" --accept "docs"` writes one op file with both criteria in payload order, exit 0.
- Title empty / 257 bytes: exit 2.
- `--parent <prefix>` ambiguous: exit 2 with `ambiguous_id` candidates.
- `--parent <unknown>`: exit 3 (`not_found`).
- `--parent <closed-id> --json`: stdout JSON contains `warnings: ["parent_closed"]`; no stderr text.
- `--parent <closed-id>` without `--json`: stderr warning printed; stdout empty/human form.
- Forced nonce collision (test hook injects collisions): retry 8x then exit 1.
- `--no-commit`: op file staged, no commit; `committed: false` in JSON.
- `--no-commit --push`: exit 2.
- `--isolated --push`: exit 2.
- `post-create` hook returning non-zero: exit 1, op file still on disk per hook contract.
- Implicit fold encounters newer `writer_version`: exit 4.
