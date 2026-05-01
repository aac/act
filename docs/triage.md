# Surface-gap triage — v0.1.0

Date: 2026-05-01
Inputs: `/home/user/act/docs/surface-gap-analysis.md`, `/home/user/act/docs/issues/act-g001-gap.md` through `act-g010-gap.md`, `/home/user/act/docs/spec-v2.md`, `/home/user/act/docs/dogfood-report.md`, `/home/user/act/docs/usability-review.md`.

Heuristics applied:
- Missing CLI driver for a spec-defined op type → strongly prefer fix-now (g002 reopen, g008 redact, g009 tombstone).
- Critical audit/recovery gap → fix-now (g001).
- "Would be nice to have flag X on a working command" → defer to v0.2 (g003, g004, g005, g006, g007).
- Nice-to-have ergonomics on a workflow that already works → defer to v0.2 (g010).
- No duplicates of the existing dogfood/usability criticals (`-p 0`, error envelope, ambiguous prefix); none rejected on that basis.

## Dispositions

| Issue | Title | Disposition | Reason |
|-------|-------|-------------|--------|
| act-g001 | act show should surface closer identity for audit | fix-now | Critical audit gap in surface-gap analysis Workflow D; `closed_by_tree` already computed/stored per spec line 681; surfacing it is mechanical and satisfies a load-bearing audit flow without log-grep. |
| act-g002 | act reopen <id> CLI command | fix-now | Spec §5.B.4 defines the `reopen` op type and §5.A.4 explicitly rejects `update --status open` on closed issues. No CLI driver means a regressed bug has no supported recovery path. Missing-CLI-driver-for-spec-op heuristic applies. |
| act-g003 | act create --blocked-by + act_file_blocker | defer-to-v0.2 | `act create` and `act dep add` both work; this is a composition/ergonomics improvement on working commands. Two-step file-and-link is workable for v0.1.0. |
| act-g004 | act mine / act ready --mine | defer-to-v0.2 | `act list --assignee=$me` is a working workaround. Convenience wrapper, not blocking. |
| act-g005 | act dep add direction aliases | defer-to-v0.2 | Today's positional `dep add` works; aliases reduce mental load but the command is functional. Direction-corruption risk is real but mitigable with docs in the meantime. |
| act-g006 | --description-file for create/update | defer-to-v0.2 | Flag enhancement on a working command; `--description "..."` continues to function. Annoying in CI but not blocking. |
| act-g007 | act_next includes commit_marker | defer-to-v0.2 | Construction of `(act-<prefix>)` is two-piece concatenation from existing fields; ergonomics gain, not correctness. |
| act-g008 | act redact CLI command | fix-now | Spec §5.A.2 defines `redact` op with detailed semantics including idempotency edge case (line 1042). No CLI driver leaves secret-leakage incident response without a supported path. Missing-CLI-driver-for-spec-op heuristic applies; the heuristic explicitly cites `act redact` as an example. |
| act-g009 | act delete <id> (tombstone) CLI command | fix-now | Spec line 378 defines `tombstone` semantics; the op type exists with no CLI driver. Erroneously-created issues have no supported removal path. Missing-CLI-driver-for-spec-op heuristic applies. |
| act-g010 | act log --summary timeline view | defer-to-v0.2 | Workflow D works without it; full `act log` output already covers forensics. Pure readability win. |

## Counts

- fix-now: 4 (g001, g002, g008, g009)
- defer-to-v0.2: 6 (g003, g004, g005, g006, g007, g010)
- reject: 0

## Top 3 fix-now to work next

1. **act-g002** — `act reopen <id>` CLI command. Spec-defined op type, no CLI driver, blocks a real recovery flow. Self-contained command implementation.
2. **act-g001** — `act show` closer identity. Critical audit gap; data already computed and stored on the index, just needs surfacing through the snapshot path.
3. **act-g008** — `act redact` CLI command. Spec-defined op type, no CLI driver, incident-response load-bearing. Slightly larger than g002 because of `--field` path parsing.

(act-g009 `act delete` follows fourth; same shape as g008 but lower frequency.)
