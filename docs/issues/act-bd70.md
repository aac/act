---
title: "ID generation and nonce protocol"
deps: [act-b545, act-1396]
acceptance_criteria:
  - "`ids.Generate(payload CreateOpPayloadCore) (FullID, Nonce, ShortID, error)` produces `full = sha256(canonicaljson(payload || nonceBytes)).hex()` (64 chars) and a fresh 16-byte crypto-random nonce."
  - "`Nonce` is rendered as exactly 32 lowercase hex characters in the op payload's `nonce` field."
  - "`FullID` matches `^act-[0-9a-f]{40}$`; `ShortID` is `act-` plus the first N hex chars of the full hex (default N=4)."
  - "`ids.ResolveCollision(layout, full)` acquires `.act/.lock`, scans `.act/ops/*` for prefix collisions whose stored `full_id` differs, and grows N by 1 until unique; documented `N <= 8` in practice but no upper limit enforced."
  - "Concurrency test: 64 parallel goroutines generating ids against the same `.act/ops/` produce 64 distinct `ShortID`s."
  - "Determinism: given the same payload AND nonce, `Generate` returns the same full id (i.e. nonce derivation is the only randomness)."
  - "Negative test: a payload that fails canonical-JSON marshalling propagates the error and produces no id."
status: open
created_at: 2026-04-29T00:00:00Z
---

# ID generation and nonce protocol

## Context
Spec §ID model defines the create-time hashing protocol. The id is a
sha256 of canonical JSON of the create payload concatenated with a 16-byte
nonce, sliced to a short hex prefix that is grown only on collision under
an advisory lock. This issue isolates id-derivation so the create command
(act-65e6) is a thin wrapper.

## Scope
- Package `internal/ids`:
  - `Generate(corePayload any) (FullID, NonceHex, ShortID, error)`.
  - `ResolveCollision(layout *store.Layout, full FullID) (ShortID, error)`.
  - `ParseFullID`, `ParseShortID`, `IsHexN(s string, n int) bool` helpers.
  - Constants: `FullHexLen = 40`, `MinShortHexLen = 4`, `NonceBytes = 16`.
- Tight integration with `canonicaljson.MarshalPayload` and
  `internal/store.Lock`.

## Out of scope
- Writing the create op file (act-6ec9).
- Display-time shortest-unique-prefix logic (act-6991).
- Bootstrap importer mapping (act-6eff).

## Implementation notes
- Hash input order matters: `canonical_payload_bytes || nonce_bytes`. The
  payload bytes are produced by `canonicaljson.MarshalPayload(corePayload)`
  AFTER the nonce hex is embedded under key `"nonce"`. Re-read §ID model
  carefully: the nonce IS part of the canonical payload, not appended raw.
  Spec literal: `full_hex = sha256(canonical_json(create_op_payload) || nonce_bytes)` —
  the canonical form contains the nonce hex string AND the bytes are then
  followed by the raw nonce bytes. Implement exactly that ordering.
- `ResolveCollision` scans `.act/ops/<prefix>*` directories: for each
  matching directory, read the create op's `full_id` and compare. Because
  full ids are derived deterministically, two distinct issues sharing a
  prefix are extremely rare but real.
- The advisory `.act/.lock` is held for the duration of scan + final
  `mkdir(.act/ops/<short_id>/)`. Release on return; do not hold across
  the op-file write.
- Nonce randomness comes from `crypto/rand`; never `math/rand`.

## Test plan
- Unit: deterministic-with-fixed-nonce vector (golden full_id given fixed
  payload+nonce).
- Unit: format checks (FullID regex, ShortID regex).
- Concurrency test: 64 goroutines call `Generate` + `ResolveCollision` on
  the same `.act/ops/` and result in 64 distinct directories.
- Synthetic collision test: pre-populate `.act/ops/act-aaaa/` with a fake
  create op carrying a different `full_id` than what `Generate` produces;
  assert N grows to 5.
- Lock test: the second `ResolveCollision` while the first holds `.act/.lock`
  blocks, then proceeds.
