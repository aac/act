---
title: "Bootstrap importer"
deps: [act-65e6, act-5651, act-bdc8, act-03f6]
acceptance_criteria:
  - "Importer reads a JSONL file (default _generated/projects/act/issues.jsonl) and processes it all-or-nothing"
  - "Step 1 validates every line: required keys, known op_type, payload shape; failure raises import_invalid_jsonl with line number and writes nothing"
  - "Step 2 replays each op via the normal write path: fresh local HLC, local node_id, fresh act-<8hex> id"
  - "Bootstrap ids referenced by later ops in the same input are rewritten to local ids using an in-process mapping table"
  - "Step 3 writes .act/imports/<iso-utc>.json with {source: 'issues.jsonl@<sha256>', mapping, imported_at_hlc}"
  - "Step 4 produces ONE git commit containing all op files plus the mapping, message 'act-import: <sha-short> <count> ops'"
  - "Hooks do NOT fire on import (per §2)"
  - "Re-running on input with a previously-seen sha exits 0 with {imported: 0, reason: 'already_imported'}"
  - "act show / act log accept either the bootstrap id or the local act id; lookup walks .act/imports/*.json in lex order, first hit wins"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Bootstrap importer

## Context
Implements §3 of "Errors, hooks, migration, bootstrap, compaction, tests" in spec-v2. The importer is how an existing issue corpus (the `_generated` JSONL produced by the v1 prototype, or any future migration) lands in an `act` repo without losing audit history and without firing hooks.

## Scope
- New command `act import [--file PATH] [--json]`.
- All-or-nothing semantics: either every line lands as a freshly-stamped op and a single commit is created, or nothing is written.
- Idempotency keyed by the sha256 of the input bytes, recorded in the mapping file's `source` field.
- Bootstrap-id resolution surfaced through `act show` and `act log` (those commands consult the mapping files).

## Out of scope
- Hook firing during import: explicitly disabled (spec §2).
- Quarantine of bad lines: there is none. Operator fixes JSONL and re-runs.
- Re-import of partially-imported state: prior runs are either complete (idempotent) or never landed (atomic rollback at validation).

## Implementation notes
- Mapping table is in-process for replay; persisted to `.act/imports/<iso-utc>.json` only after replay succeeds.
- `imported_at_hlc` records the HLC at the moment of step 3, not the max HLC of the imported ops.
- The single commit uses `--no-verify` like other op commits and includes deletions if any (none for import).
- Resolution order: lex-sorted `.act/imports/*.json`, first hit wins. The ISO-UTC filenames make lex order = chronological order.
- Unknown op_type is a step-1 validation failure (`import_invalid_jsonl`) — not a runtime fold error.
- The new HLC for each replayed op is monotonic from the local clock; the original `hlc` field on the input line is discarded (it belongs to a different node).

## Test plan
- Spec §7.7:
  - `import_idempotent` — run twice on the same JSONL; second run exits with `{imported: 0, reason: 'already_imported'}` and produces no new commits.
  - `import_malformed_jsonl` — inject a bad line (missing `op_type`); importer exits with `import_invalid_jsonl` and the affected line number; no op files written; no commit created.
  - `import_mapping_determinism` — given fixed input bytes and a pinned local node_id + clock, the mapping file is byte-identical across runs; sha256 of the mapping file is the assertion.
- Resolution test: after import, `act show <bootstrap-id>` and `act show <act-id>` return the same issue.
