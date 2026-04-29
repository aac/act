---
title: "Op type payloads and write-time validation"
deps: [act-ba09]
acceptance_criteria:
  - "Each of the 12 op types has a dedicated Go struct: `CreatePayload`, `UpdateFieldPayload`, `AddDepPayload`, `RemoveDepPayload`, `AddAcceptPayload`, `RemoveAcceptPayload`, `ClaimPayload`, `ClosePayload`, `RedactPayload`, `ImportPayload`, `MigratePayload`, `TombstonePayload`."
  - "`op.ValidatePayload(opType string, raw json.RawMessage) error` dispatches to the per-type validator and returns specific sentinel errors (`ErrTitleTooLong`, `ErrPriorityOutOfRange`, `ErrEdgeUnknown`, etc.)."
  - "Create payload validates: `title` length 1..200, `description` 0..16384, `priority` 0..3, `type` in `{task,bug,epic,chore}`, `parent` matching id regex or empty, `accept[*]` length 1..500, `nonce` 32 hex chars, `full_id` 64 hex chars."
  - "UpdateField validates `field` in the closed enum and that `value` matches the field's expected type (e.g. `priority` requires int 0..3)."
  - "`add_dep` validates `edge_type` in `{blocks, relates, supersedes}`; `remove_dep` accepts any `parent_id` and is documented as idempotent."
  - "`remove_accept` validates exactly one of `index` (>=0) or `text` (1..500 chars) is present; both-or-neither errors with `ErrRemoveAcceptShape`."
  - "Round-trip: `Marshal(Unmarshal(raw))` byte-equal for all 12 op types in `testdata/payloads/`."
status: open
created_at: 2026-04-29T00:00:00Z
---

# Op type payloads and write-time validation

## Context
Spec §Op type payloads enumerates the exact shape of each op's `payload`
field. Write-time validation MUST reject malformed payloads before they
hit disk; the spec calls out (e.g. §Validation rules) several "hard error
at write time" cases (priority range, criterion length, etc.). This issue
delivers the typed payloads + validators so command code stays declarative.

## Scope
- Package `internal/op` extension:
  - 12 payload structs.
  - `ValidatePayload(opType, raw) error` dispatcher.
  - A registration table `payloadValidators map[string]func(json.RawMessage) error`.
  - Public `Decode<Type>(raw) (<Type>Payload, error)` helpers for fold
    code to consume.

## Out of scope
- Fold-time apply functions (act-c9f0).
- Schema-version migration logic (act-5af9).
- CLI flag-to-payload construction (per-command issues in Phase 4).

## Implementation notes
- All payloads marshal back through `canonicaljson.MarshalPayload`; nested
  keys sort lexicographically (no fixed order at payload level — only the
  envelope is fixed-order).
- `field_path` parsing in `RedactPayload` accepts `description` and
  `acceptance_criteria[<n>].text`; reject anything else with
  `ErrRedactPathUnsupported`.
- `MigratePayload.transform` is `{kind, from, to}` for v1->v2 use; only
  validate shape here, not semantics. `kind` enum: `rename_field`,
  `drop_field`, `coerce_type` (others rejected).
- `ImportPayload` validates that `mapping_file` looks like a relative
  path under `.act/imports/`; do not stat the file.
- Empty/optional fields use `omitempty` in the struct tag and the
  validator treats absence as default per spec.
- Errors are typed (sentinel + wrap) so doctor (act-40ae) can categorize.

## Test plan
- Per-payload table tests: minimum valid, maximum valid, each invalid
  field independently.
- Round-trip golden files in `testdata/payloads/<op_type>.json`.
- Cross-test with envelope: `op.Validate(env)` followed by
  `op.ValidatePayload(env.OpType, env.Payload)` succeeds for all goldens.
- Negative: a `create` payload with `priority=4` fails with
  `ErrPriorityOutOfRange`; a `remove_accept` with both `index` and
  `text` fails with `ErrRemoveAcceptShape`.
- Fuzz target on `ValidatePayload` to ensure no panic on arbitrary bytes.
