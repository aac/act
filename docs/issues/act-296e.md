---
title: "Per-field LWW with status/accept/deps exceptions"
deps: [act-c9f0]
acceptance_criteria:
  - "Per-field LWW comparator returns true iff `(new.hlc.wall, new.hlc.logical, new.op_hash) > (old.hlc.wall, old.hlc.logical, old.op_hash)` lexicographically; ties on (wall, logical) are broken by op_hash; node_id is never used as a tiebreaker"
  - "Each scalar field carries its own `last_hlc` entry; a write whose tuple does not strictly exceed the existing tuple is dropped silently and state is unchanged"
  - "Status transitions follow the table: from open -> {open, in_progress, blocked, closed}; from in_progress -> {open, in_progress, blocked, closed}; from blocked -> {open, in_progress, blocked, closed}; from closed -> {closed} via close, only `reopen` op can leave closed"
  - "acceptance_criteria is a grow-shrink set keyed by criterion hash; add_accept is set-add idempotent; remove_accept is set-remove and requires the referenced hash to exist (otherwise no-op)"
  - "deps is a grow-shrink set keyed by (target_id, edge_type); add_dep and remove_dep are commutative across nodes"
  - "closed_at and closed_reason are writable only by ops whose payload also carries status=closed; LWW thereafter"
  - "Per-field last_hlc never decreases for a given (issue_id, field) across any input order (HLC monotonicity, §3.6 test 5)"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Per-field LWW with status/accept/deps exceptions

## Context
Spec §3.3 ("Per-field conflict resolution") mandates LWW per field with named exceptions. This issue implements the comparator and per-field bookkeeping consumed by every apply function in act-c9f0.

## Scope
- Implement `lww_should_apply(field, new_hlc, new_op_hash, state.last_hlc[field]) -> bool`.
- Tuple comparison: `(wall, logical, op_hash)` lexicographic. `op_hash` is hex-string lex order on the byte representation of the sha256 (per §3.3 final paragraph).
- `state.last_hlc` is a map keyed by field name (or composite key for set members).
- Scalar-field rules apply to: `title`, `description`, `priority`, `type`, `assignee`, `parent`.
- Status transition table (closed is terminal except via `reopen`):
  - `closed -> closed` allowed (LWW on closed_at/closed_reason)
  - `closed -> *` rejected unless op_type == reopen
  - within `{open, in_progress, blocked}` LWW with no transition gating
  - `claim` op forces `(assignee, status=in_progress)` atomically; claim contention winner selection is act-9824.
- Acceptance criteria:
  - Key = sha256(criterion_text)[0:16].
  - `add_accept` is idempotent set-add; identical text on a re-add is a no-op.
  - `remove_accept` resolves payload `index` or `text` to a key; if the key is absent it is a no-op (idempotent).
  - last_hlc per criterion key supports remove-vs-add ordering across concurrent writers; equal HLC cannot occur because op_hashes differ.
- Deps:
  - Key = (target_id, edge_type).
  - `add_dep` set-add; `remove_dep` set-remove all entries matching target_id (per §2.4, remove ignores edge_type).
- closed_at / closed_reason gating: writes must carry status=closed in the same op; otherwise reject at apply layer.

## Out of scope
- Atomic claim winner selection (act-9824) — this layer accepts each claim's effect under LWW; the winner check is a separate read pass.
- Redact field-path parsing (in act-c9f0).

## Implementation notes
- Encapsulate the comparator in one function so all apply paths route through it; this is the determinism choke point.
- Keep `last_hlc` as a small typed map: `map[string]struct{wall int64; logical uint32; op_hash string}` for scalars; `map[KeyTuple]...` for set members.
- Status transition gate runs before the LWW tuple compare; an HLC-newer but transition-illegal status update is dropped (do not bump last_hlc).
- For `reopen`, after applying, explicitly drop `last_hlc["status"]`, `last_hlc["closed_at"]`, `last_hlc["closed_reason"]` per §5.B.4 so subsequent old-or-new ops resolve cleanly.

## Test plan
- Unit: comparator on tuples (wall, logical, hash); 8 cases covering each ordering dimension.
- Unit: status transition table — every (from, to) cell, including reopen path.
- Unit: concurrent `add_accept` for same text from two nodes — set has one entry; tie-break by op_hash idempotent.
- Unit: `remove_accept` for non-existent key — state unchanged, no error.
- Unit: closed_at written without status=closed in same payload — rejected, state unchanged.
- Property: 10k random op sequences, assert last_hlc per (id, field) never decreases across applications (feeds §3.6 test 5).
