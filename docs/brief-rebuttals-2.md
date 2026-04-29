# Brief Rebuttals (review-2 → v3)

## Challenge 1: HLC node-id source unspecified
**Disposition:** Incorporated
**Action / Rebuttal:** Define `node_id = sha256(machine-id || git-config user.email)[0:8]`, generated on `act init` and stored in `.act/config.json`; included in op payload and filename hash so identical `(physical, logical)` from two machines cannot collide. HLC plausibility check is `max(local_wall, last_seen_hlc_in_repo)` with reject threshold of 5 minutes drift, so fresh containers catch up to repo time on first read.

## Challenge 2: Hook failure semantics undefined
**Disposition:** Incorporated
**Action / Rebuttal:** Hooks receive op JSON on stdin plus `ACT_OP_ID`, `ACT_OP_TYPE`, `ACT_ISSUE_ID`, `ACT_HOOK_PHASE` env vars; default 5s timeout (SIGTERM then SIGKILL); non-zero exit fails the op and rolls back the staged op file before commit. Hooks NEVER run during fold, replay, import, or clone — only on the writer that originally produced the op.

## Challenge 3: Auto-commit collides with host pre-commit hooks
**Disposition:** Incorporated
**Action / Rebuttal:** `act` op-commits run with `git -c core.hooksPath=/dev/null` to bypass host repo hooks; rationale documented (op commits touch only `.act/ops/**` and are validated by `act doctor`). `--verify` opt-in restores host hooks. `--no-commit` remains for batching agents.

## Challenge 4: `act_next` retry semantics under contention
**Disposition:** Incorporated
**Action / Rebuttal:** On claim loss, `act_next` performs bounded retry with exponential backoff (max 3 tries, 100ms → 400ms → 1.6s, jittered), refolding the ready queue and excluding just-lost issues each attempt. On exhaustion returns `{claimed: false, candidates: [...]}` so the caller picks the next move; no implicit retry beyond the bound.

## Challenge 5: Bootstrap JSONL schema unspecified
**Disposition:** Incorporated
**Action / Rebuttal:** Each line is one op object with the v0.1 op envelope: `{op_version, schema_version, op_type, issue_id, payload, hlc, node_id}`. Importer validates each line, replays as if locally generated (assigning fresh HLCs/filenames), and emits an id-mapping file. Schema documented inline in the brief.

## Challenge 6: Redact semantics incoherent
**Disposition:** Incorporated
**Action / Rebuttal:** The redact op is preserved as a tombstone; prior op payloads are NEVER mutated on disk (immutability invariant holds). During fold, redact ops cause snapshots and query output to render the named fields as `"<redacted>"`. Reproducibility holds because redaction is deterministic from the op log; secret removal from git history remains the documented `git-filter-repo` escape hatch.

## Challenge 7: Push policy underspecified, ops stranded locally
**Disposition:** Incorporated
**Action / Rebuttal:** `act` does NOT push by default. Auto-commit yes, auto-push no. `--push` is opt-in per command. Losing-claim local commits stay local until the next user push or compaction; no special-casing for non-claim ops. Offline / `--isolated` is the same code path: commit locally, no network.

## Challenge 8: `index-divergence` check has no oracle
**Disposition:** Incorporated
**Action / Rebuttal:** `act doctor --check index-divergence` recomputes the index from ops/snapshots into a temporary SQLite, then diffs row-by-row against `.act/index.db`. The recomputed db is the oracle. Schema-version mismatch is reported as a separate sub-check (`index-schema`) which forces a rebuild rather than a diff.
