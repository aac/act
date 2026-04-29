---
title: "Per-op-type apply functions"
deps: [act-9362]
acceptance_criteria:
  - "Registry keyed by (op_type, op_version) returns a pure apply function for every op_type listed in §2.4: create, update_field, add_dep, remove_dep, add_accept, remove_accept, claim, close, reopen, redact, import, migrate, tombstone"
  - "Each apply function is pure: given (state, op) it returns a new state without mutating its arguments and without I/O"
  - "create on an existing non-empty issue is a no-op (first-create wins; subsequent creates ignored at apply layer)"
  - "update_field with field='status' and value in {closed, in_progress} is rejected at write-time elsewhere (act-3bbe); apply layer rejects unknown field names by leaving state unchanged and recording a fold-time warning"
  - "tombstone sets state.tombstoned=true; subsequent ops on the same issue_id are parsed but skipped at the dispatch level (state unchanged after tombstone)"
  - "Adding a new op_version means adding a new registry entry; old entries remain so historical ops fold identically (verified by a golden test that pins op_version=1 forever)"
status: closed
created_at: 2026-04-29T00:00:00Z
---

# Per-op-type apply functions

## Context
Spec §3.2 step 5 dispatches `apply(state, op)` per op_type. Spec §3.3 enumerates per-field rules; this issue implements the per-op-type bodies that consume those rules. Spec §6.4 mandates a `(op_type, op_version)` registry so historical ops keep folding identically across binary upgrades.

## Scope
Implement one apply function per op_type, all version 1:
- `create`: populate scalars from payload, append `accept[]` entries (each becomes an acceptance criterion with idx 0..n-1), set `created_at = op.hlc.wall` formatted RFC3339; idempotent if state already has a create.
- `update_field`: dispatch on `payload.field` to the LWW-by-field updater (act-296e). Reject `field=status` with values `closed|in_progress` (per §5.A.4); writer enforces but apply double-checks.
- `add_dep`: set-add `(parent_id, edge_type)` to `state.deps`.
- `remove_dep`: set-remove all entries with `target_id == payload.parent_id` regardless of edge_type (idempotent).
- `add_accept`: append a new criterion with hash key; idx is current `len(acceptance)`.
- `remove_accept`: locate by `index` or by exact `text` match; remove from acceptance set; idempotent.
- `claim`: atomically set `(assignee, status=in_progress)`; the winner-selection logic lives in act-9824 and operates over the visible claim ops, but apply just records the claim's effect under LWW.
- `close`: set `status=closed`, `closed_at=op.hlc.wall`, `closed_reason=payload.reason`.
- `reopen`: clear `closed_at`, `closed_reason`, set `status=open`; resets the `last_hlc` for those three fields (per §5.B.4).
- `redact`: mark `field_path` as redacted; rendering becomes `"<redacted>"` from this HLC forward (per §3.3 redact row).
- `import`: record source_ref into state's import provenance; no scalar mutation beyond a marker.
- `migrate`: applied per §6.4; this op rewrites interpretation. Implementation here just recognizes and forwards to the migration registry (act-5af9 fills in transforms).
- `tombstone`: set `state.tombstoned=true`, `state.deleted_at=payload.deleted_at`.

Register all functions in a `(op_type, op_version) -> func` map exposed to act-9362's dispatch loop.

## Out of scope
- Per-field LWW tuple comparison and `last_hlc` bookkeeping — act-296e owns the comparator the apply functions call.
- Claim winner selection (act-9824).
- Migration transform implementations (act-5af9).
- Hook execution (act-ce9f) — apply is pure.

## Implementation notes
- Every apply function returns a fresh `IssueState`; use copy-on-write semantics so callers can keep prior states.
- For `redact`, parse `field_path` per §2.4: bare names map to scalar fields; bracketed forms `acceptance_criteria[N].text` index into the criterion list.
- For `reopen`, explicitly drop `last_hlc["closed_at"]` and `last_hlc["closed_reason"]` so a later out-of-order close can re-set them (LWW semantics persist; clearing is required so a subsequent older close can be eclipsed).
- All payload-to-state coercions go through canonical JSON to preserve byte-for-byte equivalence with goldens.

## Test plan
- Unit per op_type: state-before, op, expected state-after as table-driven test.
- Unit: tombstone halts subsequent updates (apply called with tombstoned=true returns input unchanged).
- Unit: registry returns nil for unknown (op_type, op_version); fold caller maps that to E_UNKNOWN_OP.
- Golden: fixed 12-op multi-type sequence on testdata/fold/multi/ produces a snapshot canonical-JSON.
- Negative: update_field with field=status, value=closed → apply leaves state unchanged and emits a fold warning (write-time gate is the real protection).
