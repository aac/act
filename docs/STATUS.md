# act build status

**v0.1.0 complete, ready for human verification.**

## Final tally

- **Brief:** `docs/brief-v4.md` (4 review rounds; 38 challenges resolved)
- **Spec:** `docs/spec-v2.md` (1160 lines, 3 review rounds; 21 ambiguities resolved)
- **Original 40 issues:** all closed
- **Follow-ups (post-verification round):** 11 filed
  - 2 critical from Stage 8 verification — both closed
  - 12 usability findings + 3 dogfood findings — 3 critical fixed inline; rest documented
  - 10 surface-gap issues — 4 fixed (g001, g002, g008, g009), 6 deferred to v0.2 (`milestone: v0.2`)
- **Verifications:** initial = FAIL → fix → PASS → triage → 4 critical fixes → re-verify = **PASS** (`docs/verification.md` § Re-verification 2)

## Tag

`v0.1.0` on commit `f1b7460` — needs `git push origin v0.1.0` from a token with tag-write permission.

## What got fixed in the post-verification round

| Commit | Issue |
|--------|-------|
| `5f34d61` | Index divergence after close |
| `655e539` | Flag parser stops at first positional |
| `dc7d707` | Priority 0 silently coerced to default |
| `2a071eb` | Error envelope shape unified across commands; ambiguous prefix returns id_ambiguous |
| `2b3bf3f` | act show surfaces closed_by_node for audit |
| `caa34c4` | act redact CLI command |
| `dc8ba0e` | act delete CLI command (tombstone op) |
| `068cebd` | act reopen CLI command |
| `87cdc36` | canonicaljson RawMessage passthrough — was breaking update --description round-trips |

## Deferred to v0.2

See `docs/triage.md` for full table. Six surface-gap issues marked `milestone: v0.2` in their frontmatter:
- act-g003 — `act assign` shortcut for `--assignee`
- act-g004 — `act stats` aggregate view
- act-g005 — bulk operations
- act-g006 — `act move` for parent edges
- act-g007 — extended sort/filter on list
- act-g010 — improved `act log` filtering

## Next human action

1. `git push origin v0.1.0` (token permission).
2. Release workflow at `.github/workflows/release.yml` cuts the 5-target binaries.
3. v0.2 backlog already triaged; next pipeline run targets that milestone when ready.

## Pipeline summary

| Stage | Result |
|-------|--------|
| 1. Brief review | 4 rounds; 25 + 8 + 5 + 0 challenges |
| 2. Brief revision | brief-v2 → v3 → v4 |
| 3. Spec writing | 4 parallel section-agents → spec.md (1098 lines) |
| 4. Spec review | 4 parallel reviews → 20 ambiguities; rounds 2/3 found 1 + 0 |
| 5. Spec revision | spec-v2.md (1160 lines) |
| 6. Decomposition | INDEX.md + 40 issue files |
| 7. Execution | 40/40 closed |
| 8. Final verification | FAIL → fix → PASS → adversarial review → triage → fix-now batch → **re-verify PASS** |
