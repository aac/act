---
title: "Op file naming and shard probe"
deps: [act-ba09, act-1396]
acceptance_criteria:
  - "`store.OpFilename(env op.Envelope) string` returns `<iso8601>-<hash8>-<op_type>.json` where iso8601 is `hlc.FormatWall(env.HLC.Wall)` (24 chars) and hash8 is `hex(op.Hash(env))[0:8]`."
  - "`store.ShardDir(layout, issueID, wallMs) string` returns `<root>/.act/ops/<issueID>/<YYYY-MM>/` derived from `wallMs` UTC."
  - "`store.WriteOp(layout, env) (path string, err error)` writes the canonical envelope bytes to a temp file, fsyncs, renames into the shard directory after creating it, and returns the final path."
  - "On filename collision, the writer extends the hash component to 12 hex chars; if 12 still collides, to 16; past 16 returns `ErrOpHashCollision` (statistically impossible)."
  - "After write, `WriteOp` updates `.act/config.json:last_hlc` via `config.Update`, sequencing config rename BEFORE the op rename becomes visible (op file written into a temp location first)."
  - "Crash-injection test: failure between op-write and config-update leaves the op file present and `last_hlc` stale; fold reconstructs correctly per spec §1.2."
  - "Race test: two writers writing different ops to the same shard never produce overlapping filenames; a third writer with an identical envelope+hash gets the extended-12-hex collision branch."
status: closed
created_at: 2026-04-29T00:00:00Z
---

# Op file naming and shard probe

## Context
Spec §Op file naming and §1.2 (Persistence) constrain how ops land on
disk: filename format, shard directory by HLC year-month, collision
handling via hash extension, and the config-then-op write ordering.
Without these guarantees, fold's discovery glob and the determinism
contract both break. This issue is the lone writer of bytes into
`.act/ops/`.

## Scope
- Package `internal/store`:
  - `OpFilename(env) string`, `ShardDir(layout, id, wall) string`.
  - `WriteOp(layout, env) (path, error)` — atomic write + collision probe.
  - Sentinel `ErrOpHashCollision` for the >16-hex-fail case.
- Sequencing rules:
  1. Marshal envelope canonically.
  2. Compute primary filename (8 hex). `os.Stat` the target; if exists,
     extend to 12, then 16.
  3. Write envelope to temp file in the same shard directory; fsync;
     fsync directory.
  4. `config.Update` to set `last_hlc = max(last_hlc, env.HLC)`; this
     internally write-temp+fsync+rename `config.json`.
  5. Rename temp op file to final name; fsync directory.

## Out of scope
- Generating the envelope (act-ba09 / act-3bbe).
- Auto-commit of the written file to git (act-5ca9).
- Reading ops back (fold, act-9362).

## Implementation notes
- `<hash8>` is sliced from the hex of the full op_hash bytes; do NOT
  re-hash with a different algorithm at extension time — just slice more.
- The shard directory is created via `os.MkdirAll`; this is idempotent and
  cheap per write.
- Per spec, files are NEVER moved across shards even if a later HLC walks
  back the wall clock; the write path is final.
- The ordering of step 4 vs step 5 matters: per §1.2 the config rename
  becomes visible *before* the op file rename. This means a reader between
  steps may see a `last_hlc` newer than any visible op; that is acceptable
  because `last_hlc` is a hint and fold trusts the on-disk ops as
  authority.
- Hold `.act/.lock` only across the collision probe + final rename, not
  during the bytes-to-disk fsync.

## Test plan
- Unit: `OpFilename` golden vectors for each op_type.
- Unit: `ShardDir` for HLC walls at month boundaries (Dec 31 23:59 vs
  Jan 1 00:00 UTC).
- Collision test: pre-create a file at the 8-hex name; assert writer
  extends to 12.
- Concurrency test: 32 goroutines write distinct envelopes to the same
  issue's shard concurrently — no clobbers, all files present.
- Crash-injection: inject a failure after the op temp-file write but
  before config update; on next fold, `last_hlc` recovers from the op file.
- Determinism: writing the same envelope twice produces the same final
  filename (idempotent rename of identical bytes is allowed).
