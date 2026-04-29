---
title: "Hybrid Logical Clock implementation"
deps: [act-9cad]
acceptance_criteria:
  - "`hlc.HLC` has fields `Wall int64` (unix ms UTC), `Logical uint32`, `NodeID string` (8 lowercase hex)."
  - "`Send(now, prev) HLC` matches the spec pseudocode: `wall_new = max(now, prev.wall)`; if equal, `logical = prev.logical+1`; else `logical = 0`."
  - "`Receive(now, msg, prev) HLC` matches §1.4 exactly, including the both-equal case picking `max(prev.logical, msg.logical)+1`."
  - "`Send` returns an error if `logical_new >= 2^32`."
  - "Plausibility check `Plausible(op, ref, budget)` rejects when `abs(op.Wall - ref) > budget` (default 5 min); error type `ErrHLCImplausible`."
  - "ISO formatter `FormatWall(ms) string` emits `YYYY-MM-DDTHH:MM:SS.sssZ` (24 chars exact, millisecond precision, always `Z`)."
  - "Comparator `Less(a, b HLC) bool` orders lexicographically by `(wall, logical)`; equal `(wall, logical)` returns false (tiebreak is op_hash, handled by callers)."
  - "Property test on 10 000 random sequences shows monotonically non-decreasing HLCs out of `Send`/`Receive`."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# Hybrid Logical Clock implementation

## Context
Spec §1 (Hybrid Logical Clock) defines the deterministic ordering primitive
for every op. Fold ordering, claim winner selection, and write-time
plausibility checks all bottom out here. Getting the corner cases (equal
walls, both inputs greater than `prev`, logical overflow) wrong silently
breaks fold determinism, so this issue ships an isolated, exhaustively
tested package with no I/O dependencies.

## Scope
- Package `internal/hlc`:
  - Type `HLC{Wall int64, Logical uint32, NodeID string}`.
  - `Send(now int64, prev HLC) (HLC, error)`.
  - `Receive(now int64, msg, prev HLC) (HLC, error)`.
  - `Plausible(op HLC, ref int64, budgetMs int64) error`.
  - `FormatWall(ms int64) string` and `ParseWall(s string) (int64, error)`.
  - `Less(a, b HLC) bool`.
- `NodeID` is opaque to this package; it only validates the 8-hex shape.

## Out of scope
- Persisting `last_hlc` to `.act/config.json` (act-1396).
- Generating `node_id` from `machine-id || git-config user.email`
  (act-1396 / act-b0b9).
- Op envelope embedding (act-ba09).

## Implementation notes
- Internal time uses `int64` unix milliseconds; never `time.Time` to avoid
  monotonic-clock contamination.
- `Send`/`Receive` are pure functions; tests can pass a fake `now`.
- Logical overflow: with `uint32` and millisecond walls, this is unreachable
  outside adversarial tests, but the explicit error keeps the contract honest.
- `FormatWall` MUST produce exactly 24 characters; pad fractional seconds
  with leading zeros (`.001`, `.010`, `.100`).
- `ParseWall` is strict: rejects offsets other than `Z`, rejects sub-millisecond
  precision, rejects missing `T`.
- Plausibility budget is parameter, not a constant, because the default lives
  in `.act/config.json` (`hlc_drift_budget_seconds`).

## Test plan
- Table tests for `Send`/`Receive` covering each branch in §1.3 and §1.4.
- Round-trip test: `ParseWall(FormatWall(x)) == x` for 10 000 random unix-ms
  values across the year-2000-to-2100 range.
- Negative tests: malformed wall strings, NodeID not 8 hex chars, logical
  overflow at `2^32-1 + Send`.
- Property test (act-a64e will reuse it): apply random sequences of `Send`
  and `Receive` across 8 simulated nodes, assert no produced HLC is `Less`
  than its `prev`.
- Plausibility test: `op.Wall = ref + 5*60*1000+1` errors; `ref + 5*60*1000`
  passes.
