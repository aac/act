# Brief Rebuttals (review-3 → v4)

## Challenge 1: Hook failure semantics contradict
**Disposition:** Incorporated
**Action:** Hooks run AFTER the op file is staged but BEFORE the op-commit. Non-zero exit aborts the commit and unstages the file. `ACT_HOOK_PHASE` is `pre-commit-op`. The "after the corresponding op is committed" phrasing is removed.

## Challenge 2: `core.hooksPath=/dev/null` not portable to Windows
**Disposition:** Incorporated
**Action:** Use `git commit --no-verify` instead. Cross-platform, skips pre-commit and commit-msg hooks, same rationale.

## Challenge 3: Squash invalidates `closed_by_commit`
**Disposition:** Incorporated
**Action:** Replace `closed_by_commit` with `closed_by_tree` — the git tree hash of `.act/ops/<id>/` at close time, invariant under commit squash. Doctor's symmetric check uses tree hashes.

## Challenge 4: Importer mapping file unspecified
**Disposition:** Incorporated
**Action:** Mapping file lives at `.act/imports/<iso-timestamp>.json`, committed alongside imported ops. Format: `{"source": "issues.jsonl@<sha>", "mapping": {"bootstrap-id-1": "act-a1b2...", ...}}`. Importer is idempotent on re-run (skips already-mapped source ids). `act show` and `act log` accept pre- or post-import ids and resolve via the mapping files.

## Challenge 5: Squash version gate unspecified
**Disposition:** Incorporated
**Action:** `act` refuses to squash a commit range whose op payloads include `writer_version` newer than the running binary (force-upgrade path). The squashed commit message carries `act-squash: writer_version=<max-of-range>` so downstream readers and `act doctor` can correlate.
