# Session handoff — 2026-05-17 (evening)

The coordination-plane reframe (`act-f048`) is **closed**: design landed at v2.1 in `docs/coordination-plane-design.md`, reviewed by three parallel agents on v1 plus a plan reviewer on the issue breakdown, and the 6 Phase 1 implementation issues are filed with a correct dep graph. The next session's primary work is **orchestrating Phase 1**. The previous handoff's "TOP PRIORITY = agent-push-to-main" framing is reconsidered below — it's still relevant but is no longer the only top track now that coordination-plane Phase 1 (local-only, no extra pushes) doesn't amplify it.

## What landed this session

1. **Design v2 → v2.1 written and reviewed.** `docs/coordination-plane-design.md` is the canonical spec. v1 at commit `73ff71c` was reviewed by three parallel agents (architecture / OSS-adoption / correctness); v2 (commit `07f1378`) folded their load-bearing findings; v2.1 (commit `9932488`) dropped backward-compat machinery per Andrew's "fewer than 10 projects, all mine, one machine — optimize for the future" guidance.

2. **6 Phase 1 implementation issues filed and dep-wired.** A plan reviewer caught real gaps (CONTRIBUTING template emission, Nested-git pain points doc, bundle_strategy removal, stale `--nested`-flag accept criteria from pre-v2.1 drafts); fixes were folded via close+refile for affected issues. Final shape:
   - `act-3604` — dual-handle GitOps refactor (`actGitOps` + `hostGitOps`). Foundational. Ready.
   - `act-9e8c` — host-vs-nested repo-root resolver. Blocked-by `act-3604`.
   - `act-f9a0` — widen short id from 4 → 6 hex chars before lock-in. Ready.
   - `act-c4c5` — switch marker emission to `Act-Id:` trailer + extend doctor regex. Blocked-by `act-9e8c`.
   - `act-37f7` — doctor reconcile-lite + CI-friendly no-state + `bundle_strategy` removal + CloseResult cleanup. Blocked-by `act-9e8c`, `act-c4c5`.
   - `act-c1b4` — `act init` two-repo bootstrap + gitignore defense-in-depth + CONTRIBUTING template. Blocked-by `act-9e8c`.
   - `act-9173` — migration to nested-repo + dogfood gate in this repo. Blocked-by all five above.

   `act ready` correctly surfaces `act-3604` and `act-f9a0` as the two unblocked starting points.

3. **Related issues closed.** `act-f048` (closed with derivative pointers to the 6 above). `act-8d67` (closed as superseded). `act-6c73`, `act-208e`, `act-dfa5` (closed as obsoleted — Phase 1 dissolves the noise problem they were tracking; this means the previous handoff's "don't close these yet" note is now outdated).

4. **`act redact` removal queued.** Mid-flight bug discovery (`act-0d5d`): redact on `acceptance_criteria[N].text` records the op but `act show` doesn't reflect it. Andrew's call: remove the feature entirely rather than fix — it has zero real-world usage in the ~17 days since it landed, and the bug went unnoticed because no one was exercising it. Filed as `act-8d1d` (p1, ready); `act-0d5d` closed as superseded by the removal.

5. **CLAUDE.md and global skill updates** are queued as accept criteria on the relevant Phase 1 issues, not done inline:
   - `act-c4c5` accept includes updating this repo's CLAUDE.md (commit_marker references) and the global act skill (canonical loop step 4) downstream of the trailer-form switch.
   - `act-37f7` accept includes removing `bundle_strategy` and act-a659 references from both.
   - These propagate as part of Phase 1 work; don't pre-emptively touch them.

6. **Memories saved.** New durable feedback: "for act and adjacent tools Andrew controls, skip backward-compat machinery — fewer than 10 projects, all his, one machine; optimize for the future." Stale memory cleaned up: the previously-deferred "quiet the op-log brainstorm" is closed by `act-f048`'s structural answer; the memory entry is rewritten as "resolved — don't reopen tactical-fix territory."

## Reconsidering the previous handoff's TOP PRIORITY

The 2026-05-13 handoff put agent-push-to-main as the unconditional top priority and warned that `act-f048` would *amplify* the push-fanout problem (code remote + act remote = doubled pushes per close), making the day-to-day worse not better. **v2.1 of the design resolves this concern structurally**: coordination-plane Phase 1 is local-only with no act remote, so no second push at all. The push-fanout problem only appears in Phase 2 (contributor-replicated), which is deferred. Specifically, the v2.1 design says:

> Phase 2 (contributor-replicated) is where the harder design questions live (sync failure semantics, two-remote push fanout, marker collision across multiple act states). Phase 1 doesn't depend on any of them being resolved.

So the two priority tracks (agent-push-to-main vs. coordination-plane Phase 1) are now **independent**, not stacked. Both can proceed in parallel without amplifying each other.

## What to look at first when resuming

1. **Orchestrate Phase 1 of the coordination-plane work.** Dispatch sub-agents (probably serial per this repo's CLAUDE.md default) starting with `act-3604` (dual-handle refactor) and `act-f9a0` (id widening) — both unblocked and independent. After `act-3604` lands and is reviewed, `act-9e8c` unblocks; after `act-9e8c` lands, `act-c4c5` and `act-c1b4` unblock; etc. The migration issue `act-9173` runs last as the dogfood gate. **API review checkpoint:** the plan reviewer flagged `act-3604` (dual-handle API) as the highest-risk handoff point — review its public API surface before dispatching `act-c1b4`/`act-37f7` agents that consume it.

2. **Sequence the redact removal (`act-8d1d`).** Independent of Phase 1; can dispatch anytime. Small change (~delete 3 files + remove dispatch + clean fold path). Probably worth doing first because it unblocks Andrew's "this verb is confusing when I see it" cleanup intent.

3. **Agent-push-to-main is still real but no longer amplified.** The previous handoff's TOP PRIORITY block remains technically accurate (the auto-mode classifier still blocks `act close --push` for Andrew-present Mode A sessions). The structural fix from `docs/orchestration-design.md` "Do now" item 4 is still on the table. But: it no longer needs to land *before* coordination-plane Phase 1, because Phase 1 doesn't add new pushes. Treat it as a parallel track to pick up when convenient, with `act-b90e` (version-control the act skill) paired alongside.

4. **Phase 2 of the original four-phase plan (noise-reduction synthesis) is DONE.** `act-6c73`, `act-208e`, `act-dfa5` all closed as obsoleted by `act-f048`. Don't re-open. The previous handoff said "don't close these yet" — that note is stale.

5. **Public-release adoption gates remain queued.** `act-2204` (publish for `go install`), `act-e6a5` (brew/curl), `act-8416` (Cowork), `act-4fe6` (CC Web) all still p1 / open. The coordination-plane reframe directly unblocks the OSS-adoption case (gitignored .act/, no act-op noise in public history) — so these adoption issues can move forward as Phase 1 lands. `act-2a37` (refresh README's storage description after coordination-plane lands) is the immediate doc downstream — actionable now or after Phase 1 ships, low-cost either way.

6. **Phase 3 (CLI polish + data-model bugs) remains parallelizable.** Unchanged from the previous handoff: act-4b45, act-7ecd, act-3c89, act-b891, act-982a, act-56a0, act-f800 (CLI polish); act-8c78, act-b7ad, act-492e, act-7574 (deeper bugs). Watch for `cmd/act/internal/cli` merge conflicts per CLAUDE.md.

## Reconciliation walkthrough — no longer queued

The previous handoff queued the five-edge-case reconciliation walkthrough as an Andrew-interactive design step that needed to happen before any implementation. **That conversation no longer needs to happen.** The v1 design draft enumerated five cases as load-bearing; the three-reviewer pass found that (b)/(c)/(e) are normal states doctor should recognize rather than anomalies, and only (a) and (d) need warn-and-investigate behavior. The v2 design trims to a small table (`docs/coordination-plane-design.md` § Doctor reconciliation) plus a tightened "ignore (b) only if `type=tracking` or close op carried `--no-code`" rule per the correctness reviewer's finding about silent-desync. Implementation issue `act-37f7` carries the doctor accept criteria; no separate walkthrough required.

## Key artifacts produced this session

- `docs/coordination-plane-design.md` — the canonical design at v2.1 (3 commits: `73ff71c` → `07f1378` → `9932488`).
- 8 new act issues: `act-3604`, `act-9e8c`, `act-f9a0`, `act-c4c5`, `act-37f7`, `act-c1b4`, `act-9173` (Phase 1 impl) and `act-8d1d` (redact removal).
- 5 act issue closures: `act-f048` (done), `act-8d67` (superseded), `act-6c73` / `act-208e` / `act-dfa5` (obsoleted).
- 4 act issues closed-and-refiled because `act redact` couldn't surgically fix stale accept criteria: `act-b624` → split into `act-3604` + `act-9e8c`; `act-b382` → `act-c4c5`; `act-ba3c` → `act-c1b4`; `act-603d` → `act-9173`.
- 2 memory updates: new feedback memory `feedback_act_no_backcompat_burden.md`; rewritten `project_quiet_op_log_brainstorm.md` (deferred → resolved).
- This handoff.

## Backlog state

Run `./bin/act ready` for current ordering. Top of ready after this session:

```
act-8d1d  p1  remove act redact command + op type + fold-path handling
act-3604  p1  phase 1: dual-handle GitOps refactor (actGitOps + hostGitOps)
act-f9a0  p1  phase 1: widen short id to 6 hex chars (collision-cost lock-in)
act-2f3d  p1  op filenames break Windows checkout: ':' is reserved in NTFS paths
act-5d6a  p1  cli: --branch <ref> on op commands decouples op-writing from current working tree
act-2204  p1  publish aac/act so 'go install …@latest' resolves to current code from a fresh GOPATH
act-ff5c  p1  process for keeping docs in sync with implementation (drift prevention)
act-8416  p1  make act available in Cowork sessions
act-4fe6  p1  make act available in Claude Code Web sessions
```

Five Phase 1 issues are correctly hidden as blocked (`act-9e8c`, `act-c4c5`, `act-37f7`, `act-c1b4`, `act-9173`).

## Operational notes

- **All session work was on `main` and pushed.** Final HEAD this session: `1b52b03`. The repo dogfoods itself heavily and accumulated ~50 act-op commits this session — exactly the noise the just-landed design eliminates going forward.
- **No code was written this session.** All work was design, issue tracking, review orchestration. Six sub-agents were dispatched (3 design reviewers + 1 plan reviewer + 0 implementation; 2 of the 8 task slots were placeholders for future work).
- **Pre-close hook overhead.** Each `act close` ran the full `.act/hooks/close` gate (`gofmt + vet + go test ./...` ≈ 50s). The 5+ close-and-refile cycles needed because `act redact` was broken cost ~4 minutes of test runs total. Removing redact (`act-8d1d`) plus considering whether `act update --accept` should gain a `--rm <index>` companion would prevent this in future.
- **`act redact` is currently broken** for the only field it gets exercised against (`acceptance_criteria[N].text`). Bug ticket `act-0d5d` closed in favor of the removal (`act-8d1d`); if you defer the removal, the bug ticket text has the full repro.

## Cross-references

- Coordination-plane design: `docs/coordination-plane-design.md` (v2.1, canonical).
- Previous handoff: in git history (`docs/session-handoff.md` at HEAD~N before `1b52b03`).
- Orchestration design: `docs/orchestration-design.md` (Do-now item 4 = the agent-push-to-main structural fix; still relevant but no longer blocking).
- Commit-noise design note: `docs/commit-noise-design.md` (now mostly historical; the coordination-plane design's "Relationship to other issues" section flags the pointer note that should be added at its top).
- Global act skill: `~/.claude/skills/act/SKILL.md` (Phase 1 work will update step 4 + remove act-a659 references; see `act-c4c5` and `act-37f7` accept criteria).
- Brief: `docs/brief-v4.md` (will need a §"Redact / delete" update when `act-8d1d` lands).
