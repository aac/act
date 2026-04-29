---
title: "Compaction"
deps: [act-a1f6, act-c9f0, act-5ca9]
acceptance_criteria:
  - "Trigger fires when an issue has > 50 ops in .act/ops/<id>/ OR > 30 days since last compact op (or no compact and issue > 30 days old)"
  - "flock on .act/.compact.lock is non-blocking; failure emits 'compaction_locked' warning to stderr and exits 0"
  - "Snapshot file at .act/snapshots/<id>.json contains {id, state, subsumed_ops, as_of_hlc, tree_hash}"
  - "If issue is closed and closed_at > 30 days ago, subsumed op files are deleted; otherwise retained"
  - "compact op is written at .act/ops/<id>/<yyyy-mm>/...-compact.json with snapshot_path, snapshot_tree_hash, subsumed_count"
  - "Single commit contains snapshot + compact op + any deletions, message 'act-op: <id> compact', --no-verify"
  - "Fold seeds from snapshot when snapshot tree_hash matches current ops-dir tree-hash up through as_of_hlc; otherwise full fold + index-divergence finding"
  - "Concurrent compaction loser observes contention silently and re-fires next op"
  - "compaction_locked under --json is emitted as a stdout warning, not an error envelope (per §5.D.4)"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Compaction

## Context
Implements §5 of spec-v2 "Errors, hooks, migration, bootstrap, compaction, tests". Compaction caps fold cost on long-lived issues without losing audit fidelity: the snapshot is an accelerator over an unchanged op log (until eligible for prune).

## Scope
- Trigger detection on the writer's exit path for any successful op.
- Lock acquisition on `.act/.compact.lock` via non-blocking `flock`.
- Snapshot generation, optional prune, compact-op write, single-commit packaging.
- Fold-side integration: seed from snapshot when `tree_hash` matches.
- Manual entry point via `act doctor --compact` (delegated from act-40ae).

## Out of scope
- Periodic background daemon: the trigger is opportunistic, fired by ordinary writers only.
- Snapshot serving for `act log`: log walks raw ops; snapshot is for fold acceleration only.
- Cross-issue snapshots: each compact op covers exactly one issue.

## Implementation notes
- The `tree_hash` is the git tree hash of `.act/ops/<id>/` at snapshot time; recorded both on the snapshot file and on the compact op for cross-checking.
- Pruning is gated on `closed_at > 30 days ago`; an open issue with 51 ops still gets a snapshot but keeps every op file.
- Lock release is implicit on process exit; an explicit unlock fires on success path so a long-running parent process can compact multiple issues in sequence.
- Hash-mismatch on fold falls back to full fold and emits an `index-divergence` finding for `act doctor` (act-40ae) — never silently disagrees.
- Two concurrent writers will not double-compact: filename uniqueness via op-payload nonce and node_id ensures distinct compact ops if they did, but the lock prevents the case in practice.
- Hooks do NOT fire on the compact-op commit (it is internal maintenance, not a user write).

## Test plan
- Trigger threshold: synthesize issues with 49, 50, 51 ops and assert compaction fires only on the >50 case.
- Lock contention: spawn two compactors in the same second on the same repo; one wins, the other emits `compaction_locked` to stderr and exits 0.
- Tree-hash invariant: produce a snapshot, mutate `.act/ops/<id>/` out-of-band, refold; assert full-fold fallback and `index-divergence` finding via doctor.
- Prune test: close an issue, set `closed_at` to 31 days ago, run compaction, assert subsumed op files are deleted and the snapshot retains state.
- JSON warning shape per §5.D.4: assert `compaction_locked` appears as a stdout warnings entry, not as an error envelope.
