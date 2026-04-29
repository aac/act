---
title: "Op-schema migration"
deps: [act-3bbe, act-c9f0]
acceptance_criteria:
  - "Three version axes recognized in every op: op_version, schema_version, writer_version (per §6.4)"
  - "Reader gate: op.writer_version > binary.max_known_writer_version → version_skew error (exit 4); hard refusal, not warning"
  - "Fold dispatch via apply_op registry keyed by (op_type, op_version); adding a new op_version adds an entry; old entries remain forever"
  - "act migrate writes a `migrate` op per affected issue with payload {from: {op_version, schema_version}, to: {op_version, schema_version}, transform: <named-transform-id>, applies_to_issue, writer_version}"
  - "act migrate --dry-run lists affected issues and their target transform as JSON; no writes; exit 0"
  - "Transform registry maps name → pure function (state, op) -> state; a known starter transform is `rename_field` covering the §2.4 example {kind: rename_field, from: 'owner', to: 'assignee'}"
  - "Older binaries hit version_skew on a migrate op produced by a newer binary and refuse to fold (correct behavior; forces upgrade)"
  - "Migrate ops are themselves applied via the registry; replay across binaries produces identical folded state for any input set whose ops are within the binary's known op_versions"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Op-schema migration

## Context
Spec §6.4 ("Op-schema migration") defines how `act` evolves op payloads and folded shapes without breaking existing repos. The mechanism is a `(op_type, op_version)` apply registry plus a `migrate` op type that records intent. Old binaries refuse new writer_versions; new binaries handle every old op_version forever.

## Scope
- Define version constants in the binary:
  - `MAX_KNOWN_OP_VERSION` per op_type
  - `MAX_KNOWN_SCHEMA_VERSION`
  - `MAX_KNOWN_WRITER_VERSION` (binary's own semver as the cap)
- Implement reader gate inside the fold loop (consumed by act-9362):
  - `op.writer_version > MAX_KNOWN_WRITER_VERSION` → `version_skew` error, exit 4.
- Extend the apply registry (act-c9f0) so it dispatches on `(op_type, op_version)` rather than just `op_type`. Each (op_type, version) pair is a distinct entry. Older entries are never removed — historical ops keep folding.
- Implement `act migrate <id|--all> --to <op_version> [--dry-run] [--json]`:
  - Resolve target issues (single id or every issue with at least one op below `--to`).
  - For each, build a `migrate` op envelope with the spec payload shape and the binary's writer_version.
  - `--dry-run`: emit `{"affected": [{"id":..., "transform":..., "from":..., "to":...}, ...]}`; no writes.
  - Without `--dry-run`: write each migrate op via the standard write path (auto-commit per act-5ca9).
- Define a transform registry: `name -> func(state, op) -> state`. Seed with `rename_field` covering `{kind: "rename_field", from: <old>, to: <new>}` — moves the old field's value to the new field, transferring its `last_hlc` entry, and clears the old field.
- Migrate op apply function (registered as `(migrate, 1)`):
  - Look up the named transform.
  - If unknown to this binary: emit `version_skew` (binary cannot fold ops it does not understand). This is the same error class as a too-new writer_version because the effect is identical: the user must upgrade.
  - Apply the transform pure-function-style and update `last_hlc` per affected field.

## Out of scope
- Adding new op_versions for existing op types — that is per-feature work, not this issue.
- Bootstrap importer's interaction with migrate ops (act-6eff) — bootstrap goes through the normal write path; migrate ops produced during import follow the same rules.
- Compaction's interaction with migrate ops (act-a0ad).

## Implementation notes
- Transforms are referenced by *name* on disk, never embedded as code, so two binaries with the same name agree on semantics. Document the name namespace in the source: `<binary-semver>:<transform-id>` so `0.2.0:rename_field` is distinct from a future `0.3.0:rename_field` if semantics drift.
- Per §6.4, "the binary that wrote the migrate op is the authority for what that name means at that writer_version". This means the writer_version stamp in the migrate op effectively pins the transform's semantics; older binaries with an unknown transform name OR a too-new writer_version refuse identically.
- `--all` enumerates issues with ops at op_version < target. Use the index DB (act-912f) for efficient enumeration via `SELECT id FROM issues WHERE id IN (...)` — but the source of truth is fold output.
- Migrate is one op per affected issue (not one global), so partial application across the repo is safe and resumable.

## Test plan
- Unit: registry lookup for (create, 1) and (create, 2) — both present, distinct functions.
- Unit: reader gate refuses writer_version='99.0.0' against binary='0.1.0' with exit 4.
- Unit: rename_field transform — issue with field=owner moves to assignee, last_hlc transferred.
- Unit: act migrate --dry-run on 5 affected issues emits stable JSON, no op files written.
- Unit: act migrate without --dry-run produces N migrate ops, each properly committed.
- Unit: unknown transform name in a migrate op → version_skew error, refuse to fold.
- Integration: produce migrate op with binary v0.2.0; replay on binary v0.1.0 with a smaller MAX_KNOWN_WRITER_VERSION → version_skew, exit 4.
