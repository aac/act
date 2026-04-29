---
title: "Op envelope schema and validation"
deps: [act-b545, act-9cae, act-bd70]
acceptance_criteria:
  - "Type `op.Envelope` has exported fields `OpVersion int`, `SchemaVersion int`, `WriterVersion string`, `OpType string`, `IssueID string`, `NodeID string`, `HLC hlc.HLC`, `Payload json.RawMessage`."
  - "`op.Marshal(env) ([]byte, error)` produces canonical JSON with the top-level keys in the exact order `op_version, schema_version, writer_version, op_type, issue_id, node_id, hlc, payload`."
  - "`op.Unmarshal(b []byte) (Envelope, error)` strictly parses; any unknown top-level key, missing required key, or wrong key order returns `ErrEnvelopeMalformed`."
  - "`Envelope.Validate()` enforces: `op_version == 1`, `schema_version == 1`, `op_type` in the documented enum, `issue_id` matches `^act-[0-9a-f]{4,40}$`, `node_id` matches `^[0-9a-f]{8}$`, `writer_version` parses as semver."
  - "Envelope round-trip: `Marshal(Unmarshal(b)) == b` for all golden-file fixtures."
  - "`op.Hash(env Envelope) [32]byte` returns `sha256(canonical_json(payload || hlc || node_id))` matching spec §2 step (2)."
  - "Negative test: an envelope JSON with keys reordered (e.g. `op_type` before `op_version`) fails parsing with a position-aware error."
status: open
created_at: 2026-04-29T00:00:00Z
---

# Op envelope schema and validation

## Context
Spec §Op envelope fixes both the wire shape and the canonical key order of
every op file. The op envelope is the lingua franca between writers, the
fold algorithm, and doctor checks. This issue establishes the typed Go
shape, canonical (de)serializer, and structural validator. Per-op-type
payload validation is a separate concern (act-3bbe).

## Scope
- Package `internal/op`:
  - `Envelope` struct and `OpType` constants (`OpCreate`, `OpUpdateField`,
    `OpAddDep`, `OpRemoveDep`, `OpAddAccept`, `OpRemoveAccept`, `OpClaim`,
    `OpClose`, `OpRedact`, `OpImport`, `OpMigrate`, `OpTombstone`).
  - `Marshal`, `Unmarshal`, `Validate`, `Hash`.
  - Sentinel errors `ErrEnvelopeMalformed`, `ErrUnknownOpType`,
    `ErrSchemaTooNew` (returned for `schema_version > 1`).

## Out of scope
- Per-op-type payload structs and validators (act-3bbe).
- Op file naming and shard placement on disk (act-6ec9).
- Fold-time semantics (act-9362).

## Implementation notes
- Marshal MUST go through `canonicaljson.Marshal` with a struct annotation
  (or a custom MarshalJSON) that emits the eight top-level keys in the
  fixed order. The simplest implementation is a hand-written
  `MarshalCanonical(w io.Writer, e Envelope)` that writes each field
  literally in spec order.
- Unmarshal is strict: read keys in stream order, reject duplicates,
  reject anything not in the known set, and ALSO reject if the order
  diverges from the canonical order. (Per spec, fold determinism requires
  identical bytes; an envelope whose keys are reordered cannot have been
  produced by a conforming writer.)
- `Hash` concatenates `canonicaljson(payload) || canonicaljson(hlc) || node_id_bytes_utf8`
  exactly. This is the value used as op_hash in fold tiebreaking and as
  the basis for the `<hash8>` filename component (after slicing).
- `OpType` enum lives here so act-3bbe can switch on it.

## Test plan
- Golden envelope vectors under `testdata/envelopes/`: one per op type,
  byte-identical round-trip.
- Negative tests: missing key, extra key, reordered keys, wrong type
  (string where int expected), `schema_version=2` returning
  `ErrSchemaTooNew`, unknown `op_type`.
- Hash determinism: a fixed envelope's `Hash` matches a checked-in
  golden hex value.
- Property test: random envelopes, assert `Hash` only changes when
  `payload`, `hlc`, or `node_id` changes (not `writer_version` etc.).
