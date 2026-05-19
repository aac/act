# Phase 2 dogfood: real-use validation (2026-05-19)

Closes the canonical-arc dogfood step for the Phase 2 coordination plane. Issue: act-abbf4b.

## Setup

Two clones + one bare upstream on a single host, all under `$HOME/Workspace/.dogfood-phase2-2026-05-19/`:

- `orchestrator/` — git repo whose nested `.act/.git/` was cloned from the parent act repo's `.act/.git`. `act remote enable` then `act remote add-upstream <bare>` wired in role + post-receive hook + initial upstream push.
- `upstream.git` — bare repo standing in for the GitHub fork (per ticket guidance: file-path remote is cleaner than the real fork for dogfood).
- `worker/` — git repo bootstrapped via `act bootstrap-worker --from-remote <orchestrator>/.act/.git`. `act.role=worker` written to `.act/.git/config` by the bootstrap.

The dispatched ticket was a synthetic one created in the orchestrator (`act-570d02`, "dogfood-synthetic-roundtrip", priority 3) rather than dispatching a real outstanding ticket from the parent backlog. Reasoning: claiming an actual ready ticket without implementing it would leave a status mismatch in the parent backlog. Synthetic round-trip exercises the same code paths (claim → close → push back to orchestrator) without polluting the live state.

## Steps exercised

All 7 ticket steps were exercised. Notes inline.

| Step | What | Outcome |
|---|---|---|
| 1 | `act remote enable` + `act remote add-upstream <bare>` on orchestrator | Config keys + post-receive hook written correctly; **but** the human-form output reported a doctor failure and the JSON form emitted a `doctor_failed` envelope (cause: historical orphan-close warns). State landed despite the noisy error. Filed as act-06ef97. |
| 2 | `act bootstrap-worker --from-remote <orch>/.act/.git <worker>` | Succeeded. 477 ops copied, `act.role=worker` set in the worker's nested git config, `.bootstrap-meta.json` written with dispatch_hlc. |
| 3 | Dispatch a real outstanding act issue to the worker | Substituted a synthetic ticket (see Setup). Worker pulled new ops from orchestrator via `git pull --ff-only` (rebase failed on dirty `index.db`, see step-4 finding), then `act update --claim` succeeded. |
| 4 | Worker close-and-push reaches orchestrator without harvest | `act close --no-code` ran. **First close attempt failed** because the worker's `.act/hooks/close` (copied from host) ran `go vet` on a non-Go repo. After disabling the hook, the close-then-push round-trip worked: `git push origin main` from worker → orchestrator's `receive.denyCurrentBranch=updateInstead` accepted the push → `act show act-570d02` on orchestrator reflected `status: closed` immediately. Filed as act-43cf99. |
| 5 | `act remote sync` advances upstream | Manual `act remote sync` from the orchestrator pushed orchestrator HEAD to upstream HEAD; subsequent `git rev-parse` confirmed parity. **However, the post-receive hook's automatic background sync did not advance upstream** in the run — the stale global `act` on PATH (May-16 pre-Phase 2 binary) appears to be silently no-oping the auto-sync. Filed as act-528547. |
| 6 | `act doctor` clean on both clones | After `act doctor -fix` (rebuilt divergent index from ops) + `act migrate-to-nested` (installed host pre-commit hook + ignored `.act/`), both clones report zero errors. 539 `orphan-close` warns remain on both — all artifacts of the parent repo's pre-clone commit history not being in the cloned `.act/.git`, NOT real findings about the dogfood setup. Per ticket gate criteria, case-(f)/(h) findings are allowed; these are case-(f)-class artifacts. |
| 7 | Trigger a new doctor case via `ACT_TEST_SLOW_COMMIT_MS` | `ACT_TEST_SLOW_COMMIT_MS=1500 act create ...` emitted the literal stderr `act: slow write detected (1580ms > 1000ms threshold); see .act/.slow-writes`. `.act/.slow-writes` got a JSON-line entry. Doctor's `remote_status.slow_writes_last_hour` correctly reports `1`. Also attempted to trigger case-(h) upstream-drift by creating commits ahead of upstream and lowering the threshold to 0; doctor's `upstream_drift_commits` stayed at 0 despite real drift, suggesting the comparison path needs an `origin` remote that the dogfood orchestrator didn't have. Not filed as a bug yet — needs verification that this is a config requirement vs a logic gap. |

## What worked end-to-end

- The push-attached worker→orchestrator flow is real: a worker close lands as an `act show` status change on the orchestrator in one git push, no harvest needed. `act.role=worker` plus `receive.denyCurrentBranch=updateInstead` on the orchestrator side is the load-bearing combination.
- `act bootstrap-worker --from-remote` is one command and the role config is correct out of the box.
- `act remote sync` (manual) does what it says — single command, clear output, idempotent (second invocation reports already-pushed and exits 0).
- `act remote add-upstream` correctly refuses the URL-public check (I didn't end up testing `--force-public`, but the config inspection confirms the patterns table is in play and the file-path URL was accepted as private).
- `act doctor -fix` recovered the cloned-state index-divergence without manual intervention.
- `act migrate-to-nested` is idempotent — it noticed `.act/.git` was already nested and only installed the missing host-side pre-commit hook.

## Top surprises (most-friction first)

1. **bootstrap-worker copies the host's `close` hook to the worker.** The hook assumes the host project (Go source, `go vet` runs cleanly). Workers in any non-Go workspace (or any project missing the hook's assumed deps) hit `[hook] FAIL` immediately on first close. This will hit every real `/orchestrate`-style dispatch into a non-act-repo worktree. Filed: act-43cf99.

2. **`act remote enable` exit-code and output disagree.** Human form prints "doctor reported 542 finding(s)" + exit 0; JSON form returns a `doctor_failed` envelope. The 542 findings were all orphan-close *warns* from historical commits — not a real problem. The verb is conflating "doctor exited 1" (which it does on any finding) with "enable failed." Filed: act-06ef97.

3. **Post-receive auto-sync silently no-ops if the PATH'd `act` is stale.** The hook script literally calls `nohup act remote sync ...`, depending on whichever `act` is first on PATH. The dogfood machine had a May-16 pre-Phase-2 `act` in `~/go/bin`, and the auto-sync never landed. There's no signal in the hook output or doctor output that the sync failed. The case-(h) drift check that's supposed to catch this also didn't fire (separate question — possibly a different config requirement). Filed: act-528547.

## Other findings worth knowing

- **`act search` is brittle on hyphenated and dotted phrases.** `act search "post-receive"`, `act search "index.db"`, `act search "phase-2 ticket"` all blow up with FTS5 syntax errors. Every act ticket id, every hook filename, every config key has a hyphen or a period — exactly the strings agents will grep for to check the backlog before filing. Filed: act-ad52d9.

- **Writes after reads emit misleading pull-rebase stderr.** `act show` mutates `index.db` (read cache), which is tracked in git. Subsequent writes do `git pull --rebase`, which fails with "You have unstaged changes." The write itself still lands and exit code is 0, but the stderr noise reads like a failed write. Untracking `index.db` at the nested layer would dissolve this whole class of friction. Filed: act-68f08b.

- **Doctor's case-(h) didn't fire under real drift in this dogfood.** Created 2 orchestrator commits not synced to upstream, lowered `act.upstreamDriftThresholdCommits` to 0, ran doctor: `upstream_drift_commits` reported 0. Possibly requires an `origin` remote (the orchestrator clone had no `origin` — I removed the inherited one to dodge the parent-repo push collision). If that's the design, it should be in the doctor docs. If it's a logic gap, file as a bug after verifying. Did not file pending verification.

## Bugs filed

| id | severity | summary |
|---|---|---|
| act-43cf99 | bug, p2 | bootstrap-worker copies host-specific `.act/hooks/close` to worker; breaks workers in non-host repos |
| act-528547 | bug, p2 | post-receive auto-sync hook calls bare `act` on PATH, silently no-ops with stale PATH'd act |
| act-06ef97 | bug, p2 | `act remote enable` exit-code/output mismatch on warn-only doctor findings |
| act-ad52d9 | bug, p3 | `act search` SQL/FTS5 errors on hyphens, periods, common query phrases |
| act-68f08b | bug, p2 | `act` writes leak misleading `git pull --rebase` stderr when `index.db` is dirty (exit 0 masks it) |

## Build + doctor gate

- `go build -o bin/act ./cmd/act` — succeeded (no output).
- Orchestrator `act doctor`: 0 errors, 539 warns (all case-(f)-class orphan-close warns inherited from the parent repo's commit history not being in the dogfood clone; not real findings about the dogfood setup).
- Worker `act doctor`: 0 errors, 539 warns (same).
- Orchestrator's `.act/.git` has the worker's commit on HEAD (`5bbdfad..8931e3b` after sync). Final state:
  - Orch HEAD = Upstream HEAD = `8931e3b0e41349a7b0cd34d2254d4c09ee01303f`
  - Worker HEAD = `5bbdfad09072634ed497772565bd76869fceeda0` (pushed before orchestrator's own subsequent commits; worker's view is one fast-forward behind, which is normal)

Push-attached worker-to-orchestrator round-trip is real and works end-to-end. The remaining friction is real but localized: the close-hook copy, the post-receive PATH lookup, the doctor exit-code surface, and the search FTS5 parsing.

## Cleanup

Temp dir `$HOME/Workspace/.dogfood-phase2-2026-05-19/` removed after writeup (see close commit).
