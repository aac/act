---
title: "Concurrency and rebase contention tests"
deps: [act-9824, act-5ca9, act-2f81]
acceptance_criteria:
  - "concurrent_claim_two_writers: two child processes each run act update --claim <id> against a shared bare remote; exactly one exits 0 with {claimed:true}; the other exits 5 with claim_lost and a winner field; asserted across 100 iterations"
  - "concurrent_distinct_ops: two writers update different fields (priority and description); both ops survive after git pull --rebase; final state contains both updates; no error from either writer"
  - "rebase_contention: three writers update the same field (priority) concurrently; after all rebases settle, fold output is deterministic across runs (HLC + op-hash tiebreaker); asserted by running 50 times and comparing final-state hashes"
  - "Tests use a shared bare repo on the local filesystem and spawn real git/act subprocesses (no in-process simulation)"
  - "Each test cleans its working trees and the bare repo between iterations"
  - "Failures dump op-file listings and HLC sequences to aid debugging"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Concurrency and rebase contention tests

## Context
Implements spec-v2 §7.4. The atomic claim protocol (act-9824) and auto-commit/push policy (act-5ca9) interact across multiple writers via git's rebase semantics. These tests exercise the real flow — two or three subprocesses, a shared bare remote, real `git pull --rebase` — and assert outcomes that the fold rules must guarantee deterministically.

## Scope
- Three concurrency scenarios as described in spec §7.4:
  - `concurrent_claim_two_writers` — exactly-one-winner over 100 iterations.
  - `concurrent_distinct_ops` — disjoint-field updates always co-survive.
  - `rebase_contention` — three-way same-field race; fold output deterministic over 50 iterations.
- Test harness that constructs a bare repo, clones N working trees, runs real `act` subprocesses with `--claim` / update flags, and asserts post-conditions.
- Failure diagnostics: on assertion failure, dump `.act/ops/<id>/` listing for each working tree and the HLC sequence as observed by fold.

## Out of scope
- Property and fuzz testing (act-a64e).
- Golden tests (act-9b55).
- MCP-level concurrency: the MCP `act_next` retry loop is covered in act-2f81 / §7.5.
- Cross-host networking: the shared remote is a local filesystem bare repo, not a remote server.

## Implementation notes
- Use `t.TempDir()` per iteration so cleanup is automatic; the bare repo lives at `<tmp>/remote.git`.
- The two writers spawn as `os/exec` subprocesses with distinct `node_id`s; subprocesses are real `act` binaries built once per test run.
- `rebase_contention` final-state determinism is asserted via sha256 of canonical JSON across 50 runs; any drift fails immediately.
- Inject a synchronization barrier at op-write time (env var that the writer waits on) so the three writers stamp HLCs in a controlled window — without it, the OS scheduler can serialize them and the contention is not exercised.
- Use `--claim` HLC drift check ordering per §5.C.3: the drift check runs before pull-rebase, which the test must respect when staging the race.

## Test plan
- Cite spec §7.4.
- `concurrent_claim_two_writers`: run 100 iterations; assert exactly one `{claimed:true}` and exactly one `claim_lost` with a `winner` field per iteration; total wins+losses = 200.
- `concurrent_distinct_ops`: assert post-rebase state contains both writers' field changes; assert each writer's exit code is 0; assert both ops are present in `.act/ops/<id>/`.
- `rebase_contention`: collect final-state sha256 across 50 iterations; assert all 50 hashes are identical.
- Stress smoke: run the full concurrency suite under `-race` (Go race detector) and assert no data races reported.
